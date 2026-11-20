# Home Manager module for mazama, with a Stylix target.
#
#   programs.mazama.enable = true;
#
# If Stylix is enabled, mazama follows its base16 palette (opt out with
# stylix.targets.mazama.enable = false). Otherwise, or to override it, set
# programs.mazama.style to a style name, a glamour style, or a base16 scheme.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.mazama;
  stylixOn = config.stylix.enable or false;
  bases = map (i: "base0${i}") [
    "0" "1" "2" "3" "4" "5" "6" "7" "8" "9" "A" "B" "C" "D" "E" "F"
  ];
  styleFile =
    if cfg.style == null || builtins.isString cfg.style then
      null
    else
      pkgs.writeText "mazama-style.json" (builtins.toJSON cfg.style);
in
{
  options = {
    programs.mazama = {
      enable = lib.mkEnableOption "mazama, a gopher, gemini and usenet terminal client";

      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
        defaultText = lib.literalExpression "mazama.packages.\${system}.default";
        description = "The mazama package to install.";
      };

      style = lib.mkOption {
        type = with lib.types; nullOr (either str (attrsOf anything));
        default = null;
        example = lib.literalExpression ''"dracula"'';
        description = ''
          Default style. Either the name of a built-in style (as accepted by
          `-style`), or an attribute set that is written to
          {file}`$XDG_CONFIG_HOME/mazama/style.json`: a glamour style, or a
          base16 scheme (`{ base00 = "#282c34"; … base0F = "…"; }`).
          Set automatically when the Stylix target is enabled.
        '';
      };
    };

    stylix.targets.mazama.enable = lib.mkOption {
      type = lib.types.bool;
      default = stylixOn && (config.stylix.autoEnable or true);
      defaultText = lib.literalExpression "config.stylix.enable && config.stylix.autoEnable";
      description = "Whether to theme mazama with the Stylix base16 palette.";
    };
  };

  config = lib.mkIf cfg.enable (
    lib.mkMerge [
      (lib.mkIf (config.stylix.targets.mazama.enable && stylixOn) {
        programs.mazama.style = lib.mkDefault (
          lib.genAttrs bases (b: config.lib.stylix.colors.withHashtag.${b})
        );
      })
      {
        home.packages = [ cfg.package ];
        home.sessionVariables = lib.mkIf (builtins.isString cfg.style) {
          MAZAMA_STYLE = cfg.style;
        };
        xdg.configFile."mazama/style.json" = lib.mkIf (styleFile != null) {
          source = styleFile;
        };
      }
    ]
  );
}
