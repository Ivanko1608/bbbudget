{
  description = "Multi-currency budget planner with exact money math: web UI, CLI and MCP server, fully local";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      inherit (nixpkgs) lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = self.shortRev or self.dirtyShortRev or "dev";
    in
    {
      packages = forAllSystems (pkgs: {
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.bbbudget;
        bbbudget = pkgs.buildGoModule {
          pname = "bbbudget";
          inherit version;

          src = lib.fileset.toSource {
            root = ./.;
            fileset = lib.fileset.unions [
              ./go.mod
              ./go.sum
              ./cmd
              ./internal
              ./web
              ./packaging/systemd
            ];
          };
          vendorHash = "sha256-zeqShm2OnsvuEb1XIsyDn0l9iEfeGBEjSaPh2pnbgdw=";

          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];

          postInstall = lib.optionalString pkgs.stdenv.hostPlatform.isLinux ''
            install -Dm644 packaging/systemd/bbbudget.service $out/lib/systemd/user/bbbudget.service
            substituteInPlace $out/lib/systemd/user/bbbudget.service \
              --replace-fail /usr/bin/bbbudget $out/bin/bbbudget
          '';

          meta = {
            description = "Multi-currency budget planner with exact money math: web UI, CLI and MCP server";
            homepage = "https://github.com/Ivanko1608/bbbudget";
            license = lib.licenses.agpl3Plus;
            mainProgram = "bbbudget";
            platforms = lib.platforms.unix;
          };
        };
      });

      overlays.default = final: _prev: {
        bbbudget = self.packages.${final.stdenv.hostPlatform.system}.bbbudget;
      };

      nixosModules.default =
        {
          config,
          lib,
          pkgs,
          ...
        }:
        let
          cfg = config.programs.bbbudget;
        in
        {
          options.programs.bbbudget = {
            enable = lib.mkEnableOption "bbbudget, a local multi-currency budget planner";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.bbbudget;
              defaultText = lib.literalExpression "bbbudget.packages.\${system}.bbbudget";
              description = "The bbbudget package to use.";
            };
            autoStart = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = ''
                Start the `bbbudget serve` user service at login for every user.
                When false, users opt in with `systemctl --user enable --now bbbudget`.
              '';
            };
          };

          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];
            systemd.packages = [ cfg.package ];
            systemd.user.services.bbbudget.wantedBy = lib.mkIf cfg.autoStart [ "default.target" ];
          };
        };

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go ]; };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
