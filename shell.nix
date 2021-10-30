{ forCI ? false }: let
  pkgs = import <nixpkgs> {};
in
  with pkgs;
  mkShell {
    buildInputs = [
      go
      olm
      python3Packages.yq
    ] ++ lib.lists.optional (!forCI) [
      goimports
      gopls
      vgo2nix
    ];
  }
