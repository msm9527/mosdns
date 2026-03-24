#!/bin/bash
set -euo pipefail

export GIT_TERMINAL_PROMPT=0

readonly ARTIFACT_PATTERN="${ARTIFACT_PATTERN:-mosdns*.zip}"
readonly VERSION="${VERSION:?VERSION is required}"
readonly ARTIFACT_DIR="${ARTIFACT_DIR:?ARTIFACT_DIR is required}"
readonly DEPLOY_SERVERS="${DEPLOY_SERVERS:?DEPLOY_SERVERS is required}"
readonly TARGET_PATH_OVERRIDE="${TARGET_PATH_OVERRIDE:-}"
TEMP_DIR=""

cleanup() {
  if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
    rm -rf "$TEMP_DIR"
  fi
}

mask_host() {
  local host="$1"
  echo "$host" | sed -E 's/^(.{0,3}).*(.)$/\1*******\2/'
}

collect_artifacts() {
  local source_dir="$1"
  local target_dir="$2"
  mapfile -t artifact_files < <(find "$source_dir" -type f -name "$ARTIFACT_PATTERN" | sort)
  if [ "${#artifact_files[@]}" -eq 0 ]; then
    echo "ERROR: no artifacts matched pattern ${ARTIFACT_PATTERN} under ${source_dir}" >&2
    exit 1
  fi

  for artifact in "${artifact_files[@]}"; do
    cp "$artifact" "$target_dir/"
  done
}

sync_one_server() {
  local entry="$1"
  local staging_dir="$2"
  local host=""
  local user=""
  local pass=""
  local target=""
  local port=""
  local rsync_ssh="ssh -o StrictHostKeyChecking=no"
  local -a ssh_args=(ssh -o StrictHostKeyChecking=no)

  IFS=',' read -r host user pass target port <<< "$entry"
  if [ -z "$host" ] || [ -z "$user" ] || [ -z "$pass" ]; then
    echo "ERROR: invalid DEPLOY_SERVERS entry, expected host,user,password,target_path[,port]" >&2
    exit 1
  fi

  if [ -n "$TARGET_PATH_OVERRIDE" ]; then
    target="$TARGET_PATH_OVERRIDE"
  fi

  target="${target%/}"
  if [ -z "$target" ]; then
    echo "ERROR: target_path resolved to empty value" >&2
    exit 1
  fi

  if [ -n "${port:-}" ]; then
    rsync_ssh="${rsync_ssh} -p ${port}"
    ssh_args+=(-p "$port")
  fi

  echo "同步到 $(mask_host "$host"): ${target}"
  sshpass -p "$pass" "${ssh_args[@]}" "${user}@${host}" "mkdir -p \"${target}\""
  sshpass -p "$pass" rsync -avz --delete --no-perms --no-owner --no-group \
    -e "$rsync_ssh" \
    --rsync-path="mkdir -p \"${target}\" && rsync" \
    "${staging_dir}/" "${user}@${host}:${target}/"
}

main() {
  local staging_dir=""
  local line=""
  local -a servers=()

  TEMP_DIR="$(mktemp -d)"
  staging_dir="${TEMP_DIR}/payload"
  trap cleanup EXIT

  mkdir -p "$staging_dir"
  collect_artifacts "$ARTIFACT_DIR" "$staging_dir"
  printf "%s\n" "$VERSION" > "${staging_dir}/.version"

  while IFS= read -r line; do
    line="$(echo "$line" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
    [ -z "$line" ] && continue
    servers+=("$line")
  done <<< "$DEPLOY_SERVERS"

  if [ "${#servers[@]}" -eq 0 ]; then
    echo "ERROR: DEPLOY_SERVERS is empty, expected one or more host,user,password,target_path[,port] entries" >&2
    exit 1
  fi

  echo "待同步文件:"
  ls -lha "$staging_dir"

  for line in "${servers[@]}"; do
    sync_one_server "$line" "$staging_dir"
  done
}

main "$@"
