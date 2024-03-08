{
  description = "nix bundle proot";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-23.11";

  outputs = { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ];
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      rootfsName = drv: ((drv.name or drv.pname or "image") + ".rootfs");
      prootName = drv: ((drv.name or drv.pname or "image") + ".proot");

      prootStaticBinary = pkgs.fetchurl {
        url = "https://proot.gitlab.io/proot/bin/proot";  # version latest
        hash = "sha256-t/Kt9aIlAAoWT0kFqr7+6+EcTB1b7f9eH+iGbEjdcNI=";
      };

      toDockerImage = drv: (
        pkgs.dockerTools.buildImage {
          name = rootfsName drv;
          tag = "latest";
          copyToRoot = pkgs.buildEnv {
            name = "image-root";
            pathsToLink = ["/"];
            paths = [
              pkgs.coreutils
              (if drv?outPath then drv else throw "provided installable is not a derivation and not coercible to an outPath")
            ];
          };
        }
      );

      toProot = drv: pkgs.runCommand (prootName drv) {
        nativeBuildInputs = [ pkgs.go_1_21 ];
        rootfs = toDockerImage drv;
        proot = prootStaticBinary;
        src = self;
      } ''
        export HOME=$(mktemp -d)
        mkdir build
        cd build
        cp $rootfs rootfs.tar.gz
        cp $src/main.go .
        cp $proot proot-static
        cp $src/go.mod .
        cp $src/go.sum .
        cp -rf $src/vendor .

        CGO_ENABLE=0 GOOS=linux go build -o $out ./main.go
      '';
    in
  {
    bundlers.${system} = {
      toProot = toProot;
    };
  };
}