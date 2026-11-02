# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# girlpages for Nix:
#
#   nix run github:NaomiAmethyst/girlpages#girlc -- ls    read a page
#   nix profile install github:NaomiAmethyst/girlpages    girl, girlrepo, girlc, girlserve
#
# and a NixOS module (services.girlpages) that installs it, with its config,
# and girlifies what's new daily, as the packages' systemd timer does
# elsewhere: Nix has no hooks for when packages change.
{
  description = "man pages, but make them girly";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      # As dist/version.sh has it before a release: when the commit was made.
      version = "0.0.0-git${self.lastModifiedDate or "19700101000000"}";
      pkg = "github.com/NaomiAmethyst/girlpages";

      build = pkgs: { pname, subPackages, mainProgram, description }:
        pkgs.buildGoModule {
          inherit pname version subPackages;
          src = self;
          vendorHash = "sha256-47UqafVVp2IyjVQGPfQF0xNiZ2oGhRuLQyPbgrpb6ZU=";
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s" "-w"
            "-X main.version=${version}"
            "-X ${pkg}/internal/config.SysconfDir=/etc"
            "-X ${pkg}/internal/config.SystemCacheDir=/var/cache/girlpages"
          ];
          nativeBuildInputs = [ pkgs.installShellFiles pkgs.makeWrapper ];
          # (Its tests want man, tar, node and the network; CI runs them.)
          doCheck = false;
          postInstall = ''
            for p in ${builtins.concatStringsSep " " (map baseNameOf subPackages)}; do
              installManPage man/$p.1
            done
          '' + pkgs.lib.optionalString (builtins.elem "cmd/girl" subPackages) ''
            # girl reads pages with man, and pages through less: theirs,
            # where the system has none.
            wrapProgram $out/bin/girl --suffix PATH : ${pkgs.lib.makeBinPath [ pkgs.man-db pkgs.less pkgs.groff ]}
          '';
          meta = {
            inherit description mainProgram;
            homepage = "https://github.com/NaomiAmethyst/girlpages";
            license = pkgs.lib.licenses.gpl3Plus;
          };
        };
    in
    {
      packages = forAll (pkgs: rec {
        girlpages = build pkgs {
          pname = "girlpages";
          subPackages = [ "cmd/girl" "cmd/girlrepo" "cmd/girlc" "cmd/girlserve" ];
          mainProgram = "girl";
          description = "man, but every page rewritten by an LLM in the voice of a Gen Z girl";
        };
        girlc = build pkgs {
          pname = "girlc";
          subPackages = [ "cmd/girlc" ];
          mainProgram = "girlc";
          description = "read girly man pages from a girlpages repository";
        };
        default = girlpages;
      });

      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.girlpages;
          toml = pkgs.formats.toml { };
        in
        {
          options.services.girlpages = {
            enable = lib.mkEnableOption "girlpages: girly man pages, refreshed daily";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.girlpages;
              description = "The girlpages package.";
            };
            settings = lib.mkOption {
              type = toml.type;
              default = { };
              example = { openai = { base_url = "https://api.example.org/v1"; model = "some-model"; api_key_file = "/run/secrets/girlpages"; }; };
              description = "girlpages' config (/etc/girlpages/config.toml), as girl(1) describes it.";
            };
            interval = lib.mkOption {
              type = lib.types.str;
              default = "daily";
              description = "How often to girlify what's new (a systemd calendar event).";
            };
          };
          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];
            environment.etc."girlpages/config.toml" = lib.mkIf (cfg.settings != { }) {
              source = toml.generate "girlpages-config.toml" cfg.settings;
            };
            systemd.services.girlpages-update = {
              description = "Girlify new man pages";
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              path = [ pkgs.man-db pkgs.less pkgs.groff ];
              environment.MANPATH = "/run/current-system/sw/share/man";
              serviceConfig = {
                Type = "oneshot";
                ExecStart = "${cfg.package}/bin/girl --bulk --update --prune --quiet";
                CacheDirectory = "girlpages";
                Nice = 10;
              };
            };
            systemd.timers.girlpages-update = {
              wantedBy = [ "timers.target" ];
              timerConfig = { OnCalendar = cfg.interval; Persistent = true; RandomizedDelaySec = "1h"; };
            };
          };
        };
    };
}
