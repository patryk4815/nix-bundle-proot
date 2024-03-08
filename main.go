package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"github.com/amenzhinsky/go-memexec"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed proot-static
var ProotContent []byte

//go:embed rootfs.tar.gz
var DockerRootfsContent []byte

func main() {
	/*
		create /tmp/{random}
		unpack tar.gz /tmp/{random}/rootfs
		proot -b /tmp/{random}/rootfs/nix:/nix
		run binary mainProgram
		(exit)cleanup
	*/
	ctxCancel, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctxCancel)
	stop()
	if err == nil {
		return
	}

	var targetErr *exec.ExitError
	if errors.As(err, &targetErr) {
		os.Exit(targetErr.ExitCode())
	} else {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	if len(os.Args) < 2 {
		log.Fatal("required 2 arguments, eg. arg0=binaryname, arg1=/bin/hello")
	}

	tmpDir, err := createTmp()
	if err != nil {
		return err
	}
	if os.Getenv("PROOT_NO_CLEANUP") != "1" {
		defer cleanUp(tmpDir)
	}

	err = unpackTarGz(DockerRootfsContent, tmpDir)
	if err != nil {
		return err
	}

	// run binary
	exe, err := memexec.New(ProotContent)
	if err != nil {
		return err
	}
	defer exe.Close()

	args := []string{
		"-b", fmt.Sprintf("%s:/nix", filepath.Join(tmpDir, "nix")),
		filepath.Join(tmpDir, os.Args[1]),
	}
	args = append(args, os.Args[2:]...)

	envs := []string{}
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "PATH=") {
			env = "PATH=" + filepath.Join(tmpDir, "bin") + ":" + os.Getenv("PATH")
		}
		envs = append(envs, env)
	}

	cmd := exe.CommandContext(ctx, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.WaitDelay = time.Second * 5 // after 5s send sigkill
	cmd.Env = envs

	// send KILL to child process when parent DIE
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
	}

	err = cmd.Start()
	if err != nil {
		return err
	}

	err = cmd.Wait()
	if err != nil {
		return err
	}

	return nil
}

func cleanUp(tmpDir string) error {
	return os.RemoveAll(tmpDir)
}

func createTmp() (string, error) {
	tempDir, err := os.MkdirTemp("", "rootfs")
	if err != nil {
		return "", err
	}
	return tempDir, nil
}

func unpackTarGz(tarContent []byte, dstDir string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(tarContent))
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReaderRoot := tar.NewReader(gzipReader)
	var tarReader *tar.Reader
	for {
		header, err := tarReaderRoot.Next()
		if err == io.EOF {
			return errors.New("malformed rootfs.tar.gz, layer.tar not found")
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if !strings.HasSuffix(header.Name, "/layer.tar") {
			continue
		}
		tarReader = tar.NewReader(tarReaderRoot)
		break
	}

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break // End of archive
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dstDir, header.Name)
		// Verify that the target path is within the expected directory
		if !filepath.HasPrefix(filepath.Clean(targetPath), filepath.Clean(dstDir)) {
			return fmt.Errorf("file points outside of target directory %s -> %s", targetPath, dstDir)
		}

		// Ensure that the file is not a symbolic link pointing outside of the target directory
		switch header.Typeflag {
		case tar.TypeSymlink:
			linkDest := filepath.Join(targetPath, header.Linkname)
			if !filepath.HasPrefix(filepath.Clean(linkDest), filepath.Clean(dstDir)) {
				return fmt.Errorf("symbolic link points outside of target directory: %s -> %s, targetPath=%s, link=%s", linkDest, dstDir, targetPath, header.Linkname)
			}
			err := os.Symlink(header.Linkname, targetPath)
			if err != nil {
				return err
			}
		case tar.TypeDir:
			err := os.MkdirAll(targetPath, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
		case tar.TypeReg:
			outputFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(outputFile, tarReader); err != nil {
				outputFile.Close()
				return err
			}
			outputFile.Close()
		default:
			return fmt.Errorf("file untar not supported %v", header)
		}

	}
	return nil
}
