{ forCI ? false }: let
  pkgs = import <nixpkgs> {};
in
  with pkgs;
  mkShell {
    buildInputs = [
      go
      olm
      yq
    ] ++ lib.lists.optional (!forCI) [
      goimports
      gopls
      vgo2nix
    ];
  }
