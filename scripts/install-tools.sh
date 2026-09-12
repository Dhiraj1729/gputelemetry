#!/usr/bin/env bash
set -euo pipefail
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! xcode-select -p >/dev/null 2>&1; then
  echo 'Apple Command Line Tools are required. Run xcode-select --install and complete the macOS dialog.' >&2
  exit 1
fi
brew_bin=/opt/homebrew/bin/brew
if [[ ! -x "$brew_bin" ]]; then
  echo 'Install Homebrew from https://brew.sh first, then rerun this script.' >&2
  exit 1
fi
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
"$brew_bin" bundle --file "$project_root/Brewfile"
mkdir -p "$HOME/.docker/cli-plugins"
plugin_path="$HOME/.docker/cli-plugins/docker-buildx"
if [[ ! -e "$plugin_path" && ! -L "$plugin_path" ]]; then
  ln -s /opt/homebrew/opt/docker-buildx/bin/docker-buildx "$plugin_path"
fi
docker buildx version
bash "$project_root/scripts/doctor.sh"
