{
  description = "A terminal client for gopher and gemini";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems =
        f:
        nixpkgs.lib.genAttrs [
          "x86_64-linux"
          "aarch64-linux"
          "x86_64-darwin"
          "aarch64-darwin"
        ] (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "mazama";
          version = "0.1.0";
          src = pkgs.lib.cleanSourceWith {
            src = ./.;
            filter =
              path: type:
              let
                baseName = builtins.baseNameOf path;
              in
              baseName != "docs" && baseName != ".direnv" && baseName != "result" && baseName != "mazama";
          };

          vendorHash = "sha256-URU59cpeLXABmsorpJQ97eVje+Fj6MfQY04JgH8/Mag=";

          meta = {
            description = "A terminal client for gopher and gemini";
            homepage = "https://github.com/Flonk/mazama";
            license = pkgs.lib.licenses.mit;
            mainProgram = "mazama";
          };
        };
      });

      apps = forAllSystems (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.system}.default}/bin/mazama";
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
          ];
        };
      });
    };
}
