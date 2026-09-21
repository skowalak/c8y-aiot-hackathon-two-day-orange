{
  inputs = {
    nixpkgs.url = "https://channels.nixos.org/nixpkgs-unstable/nixexprs.tar.zst";
  };

  outputs =
    inputs:
    let
      forAllSystems =
        f:
        inputs.nixpkgs.lib.genAttrs [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ] (
          system: f inputs.nixpkgs.legacyPackages.${system}
        );
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            # Packaging the microservice: gnutar/zip build the image tarball,
            # curl talks to the application API. No Docker daemon required.
            gnutar
            zip
            curl
          ];
        };
      });
    };
}
