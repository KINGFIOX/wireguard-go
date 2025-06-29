{
  description = "Dev-only flake for wireguard-go";

  inputs = {
    nixpkgs     .url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils .url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs      = import nixpkgs { inherit system; };
        go        = pkgs.go;
      in rec {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go                      # Go 编译器
            gopls                   # LSP
            go-tools                # go vet / cover / etc.
            golangci-lint           # 轻量一键 lint
            gofumpt                 # 严格格式化
            wireguard-tools         # 测试时可直接起 wg-quick
          ];

          # GOPATH 设到仓库本地，避免污染用户全局 $HOME/go
          shellHook = ''
            export GOPATH="$PWD/.gopath"
            export PATH="$GOPATH/bin:$PATH"
            echo "→ Dev shell for wireguard-go (Go ${go.version}) ready."
          '';
        };

        # 可选：供 `nix fmt`，不用时删掉
        formatter = pkgs.nixfmt-rfc-style;
      });
}

