#!/usr/bin/env bash
# =============================================================================
#  Rivolt LXC installer for Proxmox VE  (community-scripts style, standalone)
#
#  App:      Rivolt - open-source tracker for Rivian owners
#  Source:   https://github.com/apohor/rivolt
#  Base OS:  Debian 13 (Trixie) LXC
#  Stack:    Rivolt (Go binary + embedded web UI, built from source)
#            PostgreSQL (18 from PGDG, or Debian's 17), systemd service
#
#  Run on the Proxmox VE host as root:
#      bash rivolt-lxc.sh
#
#  Inside the container afterwards:
#      update-rivolt              # update to the newest release
#      update-rivolt v1.0.3       # build a specific tag (or "main")
#      update-rivolt latest --force
#
#  License: MIT. Unofficial; not affiliated with Rivolt, Rivian or
#  community-scripts.
# =============================================================================
set -Eeuo pipefail

APP="Rivolt"
BT="Proxmox VE Helper Scripts - ${APP} LXC"

# ---------- defaults ---------------------------------------------------------
var_cpu=2
var_ram=4096        # build (tsc + go) wants ~3-4 GB; runtime needs far less
var_swap=1024
var_disk=20
var_hostname="rivolt"
var_tags="car;ev;rivian"
var_port=8080

# ---------- colours / messages ----------------------------------------------
YW=$'\033[33m'; GN=$'\033[1;92m'; RD=$'\033[01;31m'; BL=$'\033[36m'; CL=$'\033[m'; BGN=$'\033[4;92m'
CM="${GN}✔${CL}"; CROSS="${RD}✖${CL}"; INFO="${BL}ℹ${CL}"
msg_info()  { echo -ne " ${YW}➜${CL} ${1}..."; }
msg_ok()    { echo -e "\r\033[K ${CM} ${GN}${1}${CL}"; }
msg_error() { echo -e "\r\033[K ${CROSS} ${RD}${1}${CL}"; }

CTID=""
CT_CREATED=0
on_error() {
  local code=$? line=$1
  msg_error "Failed (exit ${code}) at line ${line}"
  if [[ $CT_CREATED -eq 1 ]]; then
    echo -e " ${INFO} Container ${CTID} was left in place for troubleshooting."
    echo -e "    Install log: pct exec ${CTID} -- tail -n 60 /root/rivolt-install.log"
    echo -e "    Remove it:   pct stop ${CTID}; pct destroy ${CTID}"
  fi
  exit "$code"
}
trap 'on_error $LINENO' ERR

cancel() { clear; echo -e " ${CROSS} ${RD}Setup cancelled - nothing was created.${CL}"; exit 0; }

header_info() {
  clear
  cat <<"EOF"
    ____  _            ____
   / __ \(_)   ______  / / /_
  / /_/ / / | / / __ \/ / __/
 / _, _/ /| |/ / /_/ / / /_
/_/ |_/_/ |___/\____/_/\__/   Proxmox VE LXC installer

EOF
}

# ---------- whiptail helpers -------------------------------------------------
wt_msg()   { whiptail --backtitle "$BT" --title "$1" --msgbox "$2" "${3:-12}" 76; }
wt_yesno() { whiptail --backtitle "$BT" --title "$1" ${4:-} --yesno "$2" "${3:-10}" 76; }
wt_input() { whiptail --backtitle "$BT" --title "$1" --inputbox "$2" "${4:-11}" 76 "$3" 3>&1 1>&2 2>&3 || cancel; }
wt_pass()  { whiptail --backtitle "$BT" --title "$1" --passwordbox "$2" 11 76 3>&1 1>&2 2>&3 || cancel; }
# wt_radio TITLE PROMPT DEFAULT tag desc [tag desc]...
# DEFAULT "" = nothing pre-selected; the user must pick an entry.
wt_radio() {
  local title=$1 prompt=$2 def=$3; shift 3
  local args=() found=0 state choice
  while (($#)); do
    state=OFF; [[ -n $def && $1 == "$def" ]] && { state=ON; found=1; }
    args+=("$1" "$2" "$state"); shift 2
  done
  [[ -n $def && $found -eq 0 && ${#args[@]} -gt 0 ]] && args[2]=ON
  local n=$(( ${#args[@]} / 3 )); local h=$(( n > 10 ? 10 : n ))
  while :; do
    choice=$(whiptail --backtitle "$BT" --title "$title" --radiolist "$prompt\n\n(Space selects, Enter confirms)" \
      $((h + 11)) 76 "$h" "${args[@]}" 3>&1 1>&2 2>&3) || cancel
    [[ -n $choice ]] && { echo "$choice"; return; }
    wt_msg "$title" "Nothing selected - use Space to select an entry, then Enter." 8 >/dev/tty
  done
}

is_ipv4()  { [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; }
is_cidr()  { [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]]; }
is_url()   { [[ $1 =~ ^https?://[^[:space:]/]+(:[0-9]+)?(/[^[:space:]]*)?$ ]]; }
is_email() { [[ $1 =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; }
# Escape a value for a systemd EnvironmentFile ("..." with \ and " escaped)
envq() { local v=${2//\\/\\\\}; v=${v//\"/\\\"}; printf '%s="%s"\n' "$1" "$v"; }

# ---------- pre-flight -------------------------------------------------------
preflight() {
  [[ $EUID -eq 0 ]] || { msg_error "Run this script as root on the Proxmox VE host."; exit 1; }
  command -v pct >/dev/null && command -v pvesm >/dev/null || { msg_error "pct/pvesm not found - is this a Proxmox VE host?"; exit 1; }
  [[ $(dpkg --print-architecture) == amd64 ]] || { msg_error "Only amd64 hosts are supported."; exit 1; }
  command -v whiptail >/dev/null || { msg_error "whiptail is missing (apt install whiptail)."; exit 1; }
  msg_ok "Proxmox VE $(pveversion | awk -F/ '{print $2}') detected"
}

# Let the user pick a Debian 13 template from pveam and a storage to save it to
download_template() {
  msg_info "Refreshing the template catalogue"
  pveam update >/dev/null 2>&1 || true
  msg_ok "Template catalogue refreshed"
  local avail=() t
  while read -r t; do avail+=("$t" "pveam system template"); done \
    < <(pveam available --section system 2>/dev/null | awk '{print $2}' | grep -i '^debian-13' | sort -V || true)
  if ((${#avail[@]} == 0)); then wt_msg "Template" "pveam lists no Debian 13 templates." 8; return; fi
  local tpl dst stores=() name type freek
  tpl=$(wt_radio "Download template" "Template to download:" "" "${avail[@]}")
  while read -r name type _ _ _ freek _; do
    stores+=("$name" "$(printf '%-8s %6.1f GB free' "$type" "$(awk "BEGIN{print ${freek:-0}/1048576}")")")
  done < <(pvesm status --content vztmpl 2>/dev/null | awk 'NR>1')
  ((${#stores[@]})) || { wt_msg "Template" "No storage accepts container templates (vztmpl)." 8; return; }
  dst=$(wt_radio "Download template" "Storage to save ${tpl} to:" "" "${stores[@]}")
  msg_info "Downloading ${tpl} to ${dst}"
  if pveam download "$dst" "$tpl" >/tmp/rivolt-pveam.log 2>&1; then
    msg_ok "Downloaded ${tpl} to ${dst}"
  else
    msg_error "Download failed - see /tmp/rivolt-pveam.log"; sleep 3
  fi
}

# =============================================================================
#  Menus - container settings
# =============================================================================
ask_container() {
  local nextid; nextid=$(pvesh get /cluster/nextid 2>/dev/null || echo 100)
  while :; do
    CTID=$(wt_input "Container ID" "Container ID for ${APP}:" "$nextid")
    if [[ $CTID =~ ^[0-9]+$ ]] && ((CTID >= 100)) && ! pct status "$CTID" &>/dev/null && ! qm status "$CTID" &>/dev/null; then break; fi
    wt_msg "Container ID" "ID '${CTID}' is invalid (must be >= 100) or already used by a CT/VM." 8
  done

  while :; do
    HN=$(wt_input "Hostname" "Container hostname:" "$var_hostname")
    [[ $HN =~ ^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$ ]] && break
    wt_msg "Hostname" "Use letters, digits and hyphens only." 8
  done

  UNPRIV=$(wt_radio "Container type" "Unprivileged containers are safer and are recommended." "1" \
    "1" "Unprivileged (recommended)" \
    "0" "Privileged")

  while :; do CPU=$(wt_input "CPU" "CPU cores:" "$var_cpu"); [[ $CPU =~ ^[1-9][0-9]*$ ]] && break; done
  while :; do
    RAM=$(wt_input "Memory" "RAM in MiB.\nThe first build (TypeScript + Go) needs ~3-4 GB; you can lower it afterwards (1024 is plenty to run)." "$var_ram" 12)
    [[ $RAM =~ ^[0-9]+$ ]] && ((RAM >= 1024)) && break
    wt_msg "Memory" "Enter at least 1024 MiB." 8
  done
  while :; do SWAP=$(wt_input "Swap" "Swap in MiB:" "$var_swap"); [[ $SWAP =~ ^[0-9]+$ ]] && break; done
  while :; do
    DISK=$(wt_input "Disk" "Root disk size in GB (build caches are removed after install):" "$var_disk")
    [[ $DISK =~ ^[0-9]+$ ]] && ((DISK >= 8)) && break
    wt_msg "Disk" "Enter at least 8 GB." 8
  done

  # --- root filesystem storage (no default - user picks) ---
  local opts=() name type avail
  while read -r name type _ _ _ avail _; do
    opts+=("$name" "$(printf '%-8s %6.1f GB free' "$type" "$(awk "BEGIN{print ${avail:-0}/1048576}")")")
  done < <(pvesm status --content rootdir 2>/dev/null | awk 'NR>1')
  ((${#opts[@]})) || { msg_error "No storage with 'Container' (rootdir) content found."; exit 1; }
  STORAGE=$(wt_radio "Storage" "Storage for the container root disk:" "" "${opts[@]}")

  # --- template (no default - user picks; Debian 13 only) ---
  while :; do
    local tpls=() s v
    for s in $(pvesm status --content vztmpl 2>/dev/null | awk 'NR>1{print $1}'); do
      while read -r v; do [[ -n $v ]] && tpls+=("$v" "on ${s}"); done \
        < <(pvesm list "$s" --content vztmpl 2>/dev/null | awk 'NR>1{print $1}' | grep -i 'debian-13' || true)
    done
    tpls+=("download" "Download a Debian 13 template with pveam...")
    TEMPLATE=$(wt_radio "Template" "Debian 13 OS template for the container:" "" "${tpls[@]}")
    [[ $TEMPLATE != download ]] && break
    download_template
  done

  # --- network ---
  local bridges=() b
  for b in $(ip -o link show type bridge 2>/dev/null | awk -F': ' '{print $2}' | sed 's/@.*//'); do bridges+=("$b" "Linux bridge"); done
  ((${#bridges[@]})) || bridges=("vmbr0" "default")
  BRIDGE=$(wt_radio "Network bridge" "Bridge for eth0:" "vmbr0" "${bridges[@]}")

  IPMODE=$(wt_radio "IPv4" "IPv4 addressing for eth0:" "dhcp" \
    "dhcp" "DHCP (add a reservation so the URL stays stable)" \
    "static" "Static address")
  NET_IP="dhcp"; NET_GW=""
  if [[ $IPMODE == static ]]; then
    while :; do NET_IP=$(wt_input "Static IPv4" "Address in CIDR form (e.g. 192.168.1.50/24):" ""); is_cidr "$NET_IP" && break; done
    while :; do NET_GW=$(wt_input "Gateway" "IPv4 gateway (e.g. 192.168.1.1):" ""); is_ipv4 "$NET_GW" && break; done
  fi
  IP6MODE=$(wt_radio "IPv6" "IPv6 addressing for eth0:" "none" \
    "none" "Disabled / not configured" \
    "auto" "SLAAC" \
    "dhcp" "DHCPv6")
  while :; do
    VLAN=$(wt_input "VLAN" "VLAN tag (leave blank for none):" "")
    [[ -z $VLAN || ($VLAN =~ ^[0-9]+$ && VLAN -ge 1 && VLAN -le 4094) ]] && break
  done
  while :; do
    MTU=$(wt_input "MTU" "Interface MTU (leave blank for default):" "")
    [[ -z $MTU || $MTU =~ ^[0-9]{3,5}$ ]] && break
  done
  while :; do
    DNS=$(wt_input "DNS" "DNS server(s), space separated (blank = use host settings):" "")
    local ok=1 d; for d in $DNS; do is_ipv4 "$d" || [[ $d == *:* ]] || ok=0; done
    ((ok)) && break
  done
  SEARCHDOMAIN=$(wt_input "DNS search domain" "Search domain (blank = use host settings):" "")

  # --- access ---
  while :; do
    PW1=$(wt_pass "Root password" "Root password for the container\n(leave blank to generate a random one):")
    [[ -z $PW1 ]] && { ROOTPW=$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20); ROOTPW_GEN=1; break; }
    ((${#PW1} >= 5)) || { wt_msg "Root password" "Use at least 5 characters." 8; continue; }
    PW2=$(wt_pass "Root password" "Confirm root password:")
    [[ $PW1 == "$PW2" ]] && { ROOTPW=$PW1; ROOTPW_GEN=0; break; }
    wt_msg "Root password" "Passwords do not match." 8
  done
  SSH_KEY=$(wt_input "SSH public key" "Paste an SSH public key for root (optional):" "")
  if wt_yesno "SSH" "Enable root SSH login into the container?" 8 --defaultno; then SSH_ON=yes; else SSH_ON=no; fi

  if wt_yesno "Start on boot" "Start the container automatically when the host boots?" 8; then ONBOOT=1; else ONBOOT=0; fi
  TAGS=$(wt_input "Tags" "Proxmox tags (semicolon separated):" "$var_tags")

  local host_tz; host_tz=$(cat /etc/timezone 2>/dev/null || timedatectl show -p Timezone --value 2>/dev/null || echo UTC)
  while :; do
    TZONE=$(wt_input "Timezone" "Container timezone (e.g. America/Los_Angeles):" "$host_tz")
    [[ -f /usr/share/zoneinfo/$TZONE ]] && break
    wt_msg "Timezone" "Unknown timezone '${TZONE}'." 8
  done
}

# =============================================================================
#  Menus - Rivolt settings
# =============================================================================
ask_rivolt() {
  REF_MODE=$(wt_radio "${APP} version" "Which ${APP} version should be built?" "latest" \
    "latest" "Newest tagged release (recommended)" \
    "main"   "Tip of the main branch (preview)" \
    "tag"    "A specific tag, e.g. v1.0.3")
  RIVOLT_REF=$REF_MODE
  if [[ $REF_MODE == tag ]]; then
    while :; do RIVOLT_REF=$(wt_input "Tag" "Release tag:" "v1.0.3"); [[ $RIVOLT_REF =~ ^v[0-9]+\.[0-9]+\.[0-9]+.*$ ]] && break; done
  fi

  while :; do
    PORT=$(wt_input "HTTP port" "Port ${APP} listens on inside the container:" "$var_port")
    [[ $PORT =~ ^[0-9]+$ ]] && ((PORT >= 1 && PORT <= 65535)) && break
  done

  URL_MODE=$(wt_radio "Access URL" "How will you reach ${APP}? This becomes RIVOLT_BASE_URL and is used for sign-in callbacks." "lan" \
    "lan"    "http://<container-ip>:${PORT} (LAN only)" \
    "custom" "My own URL, e.g. https://rivolt.example.com (reverse proxy)")
  BASE_URL=""
  if [[ $URL_MODE == custom ]]; then
    while :; do
      BASE_URL=$(wt_input "Base URL" "Public base URL (no trailing slash).\nYour reverse proxy must forward it to http://<container-ip>:${PORT}" "https://rivolt.example.com" 12)
      BASE_URL=${BASE_URL%/}; is_url "$BASE_URL" && break
    done
  fi

  PG_VERSION=$(wt_radio "PostgreSQL" "PostgreSQL server (installed inside the container):" "18" \
    "18" "PostgreSQL 18 from apt.postgresql.org (matches upstream)" \
    "17" "PostgreSQL 17 from Debian 13")

  AUTH=$(wt_radio "Sign-in" "How do users sign in to ${APP}?" "oidc" \
    "oidc"   "Your own OIDC provider (Pocket ID, Authentik, Authelia...)" \
    "github" "GitHub OAuth app" \
    "google" "Google OAuth client" \
    "bypass" "No login: one fixed user (LAN/plain http only)")

  OIDC_NAME=""; OIDC_KIND=""; OIDC_ISSUER=""; OIDC_CID=""; OIDC_SECRET=""; OIDC_DISPLAY=""; BYPASS_USER=""
  case $AUTH in
    oidc)
      while :; do
        OIDC_NAME=$(wt_input "OIDC provider id" "Short id for the provider (lowercase letters/digits).\nIt becomes part of the callback URL: /api/auth/oidc/<id>/callback" "sso" 12)
        [[ $OIDC_NAME =~ ^[a-z][a-z0-9]{1,15}$ ]] && break
      done
      OIDC_KIND=oidc
      OIDC_DISPLAY=$(wt_input "Button label" "Text on the sign-in button:" "Single Sign-On")
      while :; do OIDC_ISSUER=$(wt_input "Issuer URL" "OIDC issuer URL (e.g. https://id.example.com):" ""); OIDC_ISSUER=${OIDC_ISSUER%/}; is_url "$OIDC_ISSUER" && break; done
      ;;
    github) OIDC_NAME=github; OIDC_KIND=oauth2-github; OIDC_DISPLAY="GitHub" ;;
    google) OIDC_NAME=google; OIDC_KIND=oidc; OIDC_DISPLAY="Google"; OIDC_ISSUER="https://accounts.google.com" ;;
    bypass)
      wt_msg "Warning" "Upstream labels this mode debug-only: every request is signed in as one user with no credential check, and it only works over plain http (secure cookies off).\n\nUse it only on a trusted LAN, never behind a public URL." 13
      while :; do BYPASS_USER=$(wt_input "User" "Username (or email) for the single built-in user:" "admin"); [[ -n $BYPASS_USER ]] && break; done
      ;;
  esac
  if [[ $AUTH != bypass ]]; then
    while :; do OIDC_CID=$(wt_input "Client ID" "${OIDC_DISPLAY} client ID:" ""); [[ -n $OIDC_CID ]] && break; done
    OIDC_SECRET=$(wt_pass "Client secret" "${OIDC_DISPLAY} client secret (blank for a public PKCE client):")

    while :; do
      ADMIN_EMAIL=$(wt_input "Admin email" "Email of the first admin (must be VERIFIED at your provider).\nBlank = whoever signs in first becomes admin." "" 12)
      [[ -z $ADMIN_EMAIL ]] || is_email "$ADMIN_EMAIL" && break
    done
    if wt_yesno "Sign-ups" "Close sign-ups?\n\nYes: only the admin email above (or users an admin creates) can get an account, even if others can log in at your provider.\nNo: anyone who can authenticate gets an account." 13; then
      SIGNUP_DISABLED=true; else SIGNUP_DISABLED=false; fi
  else
    ADMIN_EMAIL=""; SIGNUP_DISABLED=false
  fi

  # secure cookies
  if [[ $AUTH == bypass ]]; then SECURE_COOKIE=false
  elif [[ $BASE_URL == https://* ]]; then SECURE_COOKIE=true
  else SECURE_COOKIE=false; fi

  # --- optional AI ---
  OPENAI_KEY=""; ANTHROPIC_KEY=""; GEMINI_KEY=""
  local ai
  ai=$(whiptail --backtitle "$BT" --title "AI commentary (optional)" --checklist \
    "Trip analysis and drive recaps can use an AI provider (chosen later in the admin UI). Pick any whose API key you want to add:\n\n(Space toggles, Enter confirms)" \
    16 76 3 "openai" "OpenAI" OFF "anthropic" "Anthropic" OFF "gemini" "Google Gemini" OFF 3>&1 1>&2 2>&3) || cancel
  [[ $ai == *openai* ]]    && OPENAI_KEY=$(wt_pass "OpenAI" "OpenAI API key:")
  [[ $ai == *anthropic* ]] && ANTHROPIC_KEY=$(wt_pass "Anthropic" "Anthropic API key:")
  [[ $ai == *gemini* ]]    && GEMINI_KEY=$(wt_pass "Gemini" "Google Gemini API key:")

  # --- optional geocoder ---
  PHOTON_URL=""
  case $(wt_radio "Address search" "Street-level address search uses a Photon geocoder. Without it, search is city-level only." "none" \
      "none"   "City-level search only" \
      "komoot" "Public photon.komoot.io (searches are sent to komoot)" \
      "custom" "My own Photon server") in
    komoot) PHOTON_URL="https://photon.komoot.io" ;;
    custom) while :; do PHOTON_URL=$(wt_input "Photon URL" "Photon base URL:" "http://photon.lan:2322"); is_url "$PHOTON_URL" && break; done ;;
  esac

  while :; do
    VAPID_EMAIL=$(wt_input "Push notifications" "Contact email for Web Push (VAPID subject). Optional.\nPush to phones needs the app served over HTTPS." "${ADMIN_EMAIL}" 12)
    [[ -z $VAPID_EMAIL ]] || is_email "$VAPID_EMAIL" && break
  done
}

confirm() {
  local net="ip=${NET_IP}"; [[ -n $NET_GW ]] && net+=", gw=${NET_GW}"; [[ -n $VLAN ]] && net+=", vlan=${VLAN}"
  local url=${BASE_URL:-"http://<container-ip>:${PORT}"}
  local auth=$AUTH; [[ $AUTH == bypass ]] && auth="bypass (user: ${BYPASS_USER})"
  whiptail --backtitle "$BT" --title "Confirm settings" --yesno "\
Container
  ID / hostname : ${CTID} / ${HN}  ($([[ $UNPRIV == 1 ]] && echo unprivileged || echo privileged))
  CPU/RAM/swap  : ${CPU} cores / ${RAM} MiB / ${SWAP} MiB
  Disk          : ${DISK} GB on ${STORAGE}
  Template      : ${TEMPLATE##*/}
  Network       : ${BRIDGE}, ${net}, ipv6=${IP6MODE}
  Timezone      : ${TZONE}   Start on boot: $([[ $ONBOOT == 1 ]] && echo yes || echo no)   SSH: ${SSH_ON}

${APP}
  Version       : ${RIVOLT_REF}
  URL           : ${url}
  PostgreSQL    : ${PG_VERSION}
  Sign-in       : ${auth}
  Secure cookie : ${SECURE_COOKIE}   Sign-ups closed: ${SIGNUP_DISABLED}
  Admin email   : ${ADMIN_EMAIL:-<first user>}

Create the container and install ${APP} now?" 25 78 || cancel
}

# =============================================================================
#  Container creation
# =============================================================================
create_container() {
  local net="name=eth0,bridge=${BRIDGE},ip=${NET_IP}"
  [[ -n $NET_GW ]] && net+=",gw=${NET_GW}"
  [[ $IP6MODE != none ]] && net+=",ip6=${IP6MODE}"
  [[ -n $VLAN ]] && net+=",tag=${VLAN}"
  [[ -n $MTU ]] && net+=",mtu=${MTU}"

  local args=(
    --hostname "$HN" --cores "$CPU" --memory "$RAM" --swap "$SWAP"
    --rootfs "${STORAGE}:${DISK}" --net0 "$net" --ostype debian
    --unprivileged "$UNPRIV" --features "nesting=1$([[ $UNPRIV == 1 ]] && echo ',keyctl=1')"
    --onboot "$ONBOOT" --timezone "$TZONE" --password "$ROOTPW"
  )
  [[ -n $TAGS ]] && args+=(--tags "$TAGS")
  [[ -n $DNS ]] && args+=(--nameserver "$DNS")
  [[ -n $SEARCHDOMAIN ]] && args+=(--searchdomain "$SEARCHDOMAIN")
  local keyfile=""
  if [[ -n $SSH_KEY ]]; then keyfile=$(mktemp); echo "$SSH_KEY" >"$keyfile"; args+=(--ssh-public-keys "$keyfile"); fi

  msg_info "Creating LXC container ${CTID}"
  pct create "$CTID" "$TEMPLATE" "${args[@]}" >/tmp/rivolt-pct-create.log 2>&1 || {
    msg_error "pct create failed:"; cat /tmp/rivolt-pct-create.log; [[ -n $keyfile ]] && rm -f "$keyfile"; exit 1; }
  [[ -n $keyfile ]] && rm -f "$keyfile"
  CT_CREATED=1
  msg_ok "Created LXC container ${CTID} (${HN}) on ${STORAGE}"

  msg_info "Starting container"
  pct start "$CTID"
  msg_ok "Started container"

  msg_info "Waiting for network"
  local i
  for i in $(seq 1 60); do
    CT_IP=$(pct exec "$CTID" -- sh -c "ip -4 -o addr show dev eth0 2>/dev/null | awk '{print \$4}' | cut -d/ -f1 | head -n1" || true)
    if [[ -n $CT_IP ]] && pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1; then break; fi
    sleep 2
  done
  [[ -n $CT_IP ]] || { msg_error "Container did not get an IPv4 address."; exit 1; }
  pct exec "$CTID" -- getent hosts github.com >/dev/null 2>&1 || { msg_error "Container has an IP (${CT_IP}) but no DNS/internet."; exit 1; }
  msg_ok "Network ready (${CT_IP})"
}

# =============================================================================
#  Files pushed into the container
# =============================================================================
write_files() {
  WORK=$(mktemp -d)
  [[ -z $BASE_URL ]] && BASE_URL="http://${CT_IP}:${PORT}"

  local PGPASS KEK COOKIE
  PGPASS=$(openssl rand -hex 24)
  KEK="k1:$(openssl rand -base64 32)"
  COOKIE=$(openssl rand -hex 32)

  # ---- build.conf (root-only) ----
  cat >"$WORK/build.conf" <<EOF
PG_VERSION=${PG_VERSION}
RIVOLT_REF=${RIVOLT_REF}
PORT=${PORT}
SSH_ON=${SSH_ON}
SSH_PW=$([[ -n $SSH_KEY ]] && echo no || echo yes)
PGPASS=${PGPASS}
EOF

  # ---- rivolt.env (systemd EnvironmentFile) ----
  {
    echo "# Rivolt configuration - edit, then: systemctl restart rivolt"
    echo "# All variables: https://github.com/apohor/rivolt (README / cmd/rivolt/main.go)"
    envq ADDR ":${PORT}"
    envq DATA_DIR "/var/lib/rivolt"
    envq TZ "$TZONE"
    envq DATABASE_URL "postgres://rivolt:${PGPASS}@127.0.0.1:5432/rivolt?sslmode=disable"
    echo
    echo "# Envelope-encryption key for stored Rivian credentials. BACK THIS UP."
    echo "# Losing it makes stored Rivian logins unreadable (users must reconnect)."
    envq RIVOLT_KEK "$KEK"
    envq RIVOLT_COOKIE_SECRET "$COOKIE"
    envq RIVOLT_SECURE_COOKIE "$SECURE_COOKIE"
    envq RIVOLT_BASE_URL "$BASE_URL"
    envq RIVOLT_PUBLIC_BASE_URL "$BASE_URL"
    envq RIVOLT_MIGRATE_ON_START "true"
    envq RIVOLT_WEB_CARRY_DIR "/opt/rivolt/web-carry"
    echo
    echo "# --- Sign-in ---"
    if [[ $AUTH == bypass ]]; then
      envq RIVOLT_AUTH_BYPASS_USER "$BYPASS_USER"
    else
      local P="RIVOLT_OIDC_${OIDC_NAME^^}"
      envq RIVOLT_OIDC_PROVIDERS "$OIDC_NAME"
      envq "${P}_KIND" "$OIDC_KIND"
      [[ -n $OIDC_ISSUER ]] && envq "${P}_ISSUER" "$OIDC_ISSUER"
      envq "${P}_CLIENT_ID" "$OIDC_CID"
      [[ -n $OIDC_SECRET ]] && envq "${P}_CLIENT_SECRET" "$OIDC_SECRET"
      envq "${P}_DISPLAY_NAME" "$OIDC_DISPLAY"
      envq RIVOLT_SIGNUP_DISABLED "$SIGNUP_DISABLED"
      [[ -n $ADMIN_EMAIL ]] && envq RIVOLT_BOOTSTRAP_ADMIN_EMAIL "$ADMIN_EMAIL"
    fi
    echo
    echo "# --- Optional integrations ---"
    [[ -n $OPENAI_KEY ]] && envq OPENAI_API_KEY "$OPENAI_KEY"
    [[ -n $ANTHROPIC_KEY ]] && envq ANTHROPIC_API_KEY "$ANTHROPIC_KEY"
    [[ -n $GEMINI_KEY ]] && envq GEMINI_API_KEY "$GEMINI_KEY"
    [[ -n $PHOTON_URL ]] && envq RIVOLT_PHOTON_BASE_URL "$PHOTON_URL"
    [[ -n $VAPID_EMAIL ]] && envq VAPID_SUBJECT "mailto:${VAPID_EMAIL}"
    true
  } >"$WORK/rivolt.env"

  # ---- systemd unit ----
  cat >"$WORK/rivolt.service" <<EOF
[Unit]
Description=Rivolt - Rivian companion
Documentation=https://github.com/apohor/rivolt
After=network-online.target postgresql.service
Wants=network-online.target
Requires=postgresql.service

[Service]
Type=simple
User=rivolt
Group=rivolt
EnvironmentFile=/etc/rivolt/rivolt.env
WorkingDirectory=/var/lib/rivolt
ExecStart=/opt/rivolt/rivolt
Restart=on-failure
RestartSec=5
TimeoutStopSec=90
NoNewPrivileges=true
$( ((PORT < 1024)) && echo "AmbientCapabilities=CAP_NET_BIND_SERVICE" )

[Install]
WantedBy=multi-user.target
EOF

  # ---- rivolt-build: clones + builds a ref into /opt/rivolt/rivolt.new ----
  cat >"$WORK/rivolt-build" <<'EOF'
#!/usr/bin/env bash
# Usage: rivolt-build <latest|main|vX.Y.Z>
# Builds Rivolt from source into /opt/rivolt/rivolt.new (+ .version).
set -Eeuo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
REPO=https://github.com/apohor/rivolt.git
REF=${1:-latest}
SRC=/opt/rivolt/src
LOG=${RIVOLT_BUILD_LOG:-/var/log/rivolt-build.log}
CACHE=/var/cache/rivolt-build
q()   { "$@" >>"$LOG" 2>&1; }
say() { echo "    · $*" | tee -a "$LOG"; }
die() { echo "    ✖ $*" | tee -a "$LOG" >&2; exit 1; }
trap 'echo "    ✖ build failed at line $LINENO (exit $?)" | tee -a "$LOG" >&2; tail -n 30 "$LOG"' ERR
say "rivolt-build started (ref: ${REF})"

if [[ $REF == latest ]]; then
  REF=$(git ls-remote --tags --refs "$REPO" | awk -F/ '{print $3}' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true)
  [[ -n $REF ]] || die "could not resolve the latest release tag (git ls-remote ${REPO})"
fi
say "Fetching ${REF}"
rm -rf "$SRC"
q git clone --depth 1 --branch "$REF" "$REPO" "$SRC"
cd "$SRC"
if [[ $REF == v* ]]; then VERSION=$REF; else VERSION="${REF}-$(git rev-parse --short HEAD)"; fi

# Go toolchain pinned by go.mod
GOV=$(awk '$1=="toolchain"{print $2}' go.mod)
[[ -n $GOV ]] || GOV="go$(awk '$1=="go"{print $2}' go.mod)"
if [[ "$(/usr/local/go/bin/go env GOVERSION 2>/dev/null || true)" != "$GOV" ]]; then
  say "Installing ${GOV}"
  rm -rf /usr/local/go
  curl -fsSL "https://go.dev/dl/${GOV}.linux-amd64.tar.gz" -o /tmp/go.tgz 2>>"$LOG" || die "could not download ${GOV} from go.dev"
  tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local CGO_ENABLED=0
export GOCACHE=$CACHE/go-build GOMODCACHE=$CACHE/gomod GOPATH=$CACHE/gopath
export NODE_OPTIONS=--max-old-space-size=3072 npm_config_cache=$CACHE/npm

say "Building web UI (node $(node -v))"
mkdir -p internal/web
(cd web && q npm ci --no-audit --no-fund && q npm run build)

say "Building server (${GOV})"
q go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /opt/rivolt/rivolt.new ./cmd/rivolt
echo "$VERSION" >/opt/rivolt/rivolt.new.version

cd /
rm -rf "$SRC"
[[ ${KEEP_BUILD_CACHE:-0} == 1 ]] || rm -rf "$CACHE"
say "Built ${VERSION}"
EOF

  # ---- update-rivolt ----
  cat >"$WORK/update-rivolt" <<'EOF'
#!/usr/bin/env bash
# Usage: update-rivolt [latest|main|vX.Y.Z] [--force]
set -Eeuo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
source /etc/rivolt/build.conf
REPO=https://github.com/apohor/rivolt.git
REF=${1:-latest}; FORCE=${2:-}
CUR=$(cat /opt/rivolt/VERSION 2>/dev/null || echo none)
TARGET=$REF
if [[ $REF == latest ]]; then
  TARGET=$(git ls-remote --tags --refs "$REPO" | awk -F/ '{print $3}' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n1 || true)
  [[ -n $TARGET ]] || { echo " ✖ could not resolve the latest release tag"; exit 1; }
fi
if [[ $TARGET == "$CUR" && $FORCE != --force ]]; then echo " ✔ Rivolt is already at ${CUR}"; exit 0; fi
echo " ➜ Updating Rivolt ${CUR} → ${TARGET}"

echo " ➜ Updating OS packages"
apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get -y -qq upgrade >/dev/null

mkdir -p /var/backups/rivolt
BK=/var/backups/rivolt/rivolt-${CUR}-$(date +%Y%m%d-%H%M%S).sql.gz
echo " ➜ Backing up database to ${BK}"
(cd /tmp && runuser -u postgres -- pg_dump -Fp rivolt) | gzip >"$BK"
ls -1t /var/backups/rivolt/*.sql.gz 2>/dev/null | tail -n +6 | xargs -r rm -f   # keep 5

echo " ➜ Building ${TARGET}"
/usr/local/bin/rivolt-build "$TARGET"

systemctl stop rivolt
cp -a /opt/rivolt/rivolt /opt/rivolt/rivolt.prev
cp -a /opt/rivolt/VERSION /opt/rivolt/VERSION.prev 2>/dev/null || true
mv /opt/rivolt/rivolt.new /opt/rivolt/rivolt
mv /opt/rivolt/rivolt.new.version /opt/rivolt/VERSION
systemctl start rivolt

for i in $(seq 1 90); do curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/healthz" && break; sleep 2; done
if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/healthz"; then
  echo " ✔ Rivolt $(cat /opt/rivolt/VERSION) is running"
else
  echo " ✖ New version did not become healthy - rolling back the binary"
  systemctl stop rivolt
  mv /opt/rivolt/rivolt.prev /opt/rivolt/rivolt
  mv /opt/rivolt/VERSION.prev /opt/rivolt/VERSION 2>/dev/null || true
  systemctl start rivolt
  echo "   If the new version already migrated the schema, restore the database from:"
  echo "   ${BK}   (journalctl -u rivolt -n 100 shows why it failed)"
  exit 1
fi
EOF

  # ---- in-container installer ----
  cat >"$WORK/rivolt-install.sh" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8
LOG=/root/rivolt-install.log
export RIVOLT_BUILD_LOG=$LOG
: >"$LOG"
source /etc/rivolt/build.conf
q()    { "$@" >>"$LOG" 2>&1; }
step() { echo "  ➜ $*" | tee -a "$LOG"; }
trap 'rc=$?; echo "  ✖ install failed at line $LINENO: ${BASH_COMMAND} (exit $rc)" | tee -a "$LOG"; tail -n 30 "$LOG"' ERR

step "Updating Debian"
q apt-get update
q apt-get -y full-upgrade
q apt-get install -y --no-install-recommends ca-certificates curl git gnupg openssl locales tzdata

step "Installing PostgreSQL ${PG_VERSION}"
if [[ $PG_VERSION == 18 ]]; then
  install -d /usr/share/postgresql-common/pgdg
  curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc
  echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt trixie-pgdg main" \
    >/etc/apt/sources.list.d/pgdg.list
  q apt-get update
fi
q apt-get install -y "postgresql-${PG_VERSION}"
systemctl enable --now postgresql >/dev/null 2>&1
for i in $(seq 1 30); do runuser -u postgres -- pg_isready -q && break; sleep 1; done
cd /tmp
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -q <<SQL >>"$LOG" 2>&1
DO \$\$BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'rivolt') THEN
    CREATE ROLE rivolt LOGIN PASSWORD '${PGPASS}';
  ELSE
    ALTER ROLE rivolt PASSWORD '${PGPASS}';
  END IF;
END\$\$;
SQL
runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='rivolt'" | grep -q 1 \
  || q runuser -u postgres -- createdb -O rivolt -E UTF8 -T template0 --locale=C.UTF-8 rivolt
cd /

step "Installing Node.js 24"
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor --yes -o /etc/apt/keyrings/nodesource.gpg
echo "deb [signed-by=/etc/apt/keyrings/nodesource.gpg] https://deb.nodesource.com/node_24.x nodistro main" \
  >/etc/apt/sources.list.d/nodesource.list
q apt-get update
q apt-get install -y nodejs

step "Creating rivolt user and directories"
id rivolt &>/dev/null || q useradd --system --home-dir /var/lib/rivolt --shell /usr/sbin/nologin rivolt
q install -d -o rivolt -g rivolt -m 0750 /var/lib/rivolt
q install -d -m 0755 /opt/rivolt /opt/rivolt/web-carry
q chown root:rivolt /etc/rivolt /etc/rivolt/rivolt.env
q chmod 0750 /etc/rivolt
q chmod 0640 /etc/rivolt/rivolt.env
q chmod 0600 /etc/rivolt/build.conf

step "Building Rivolt (${RIVOLT_REF}) - this takes several minutes"
[[ -x /usr/local/bin/rivolt-build ]] || { echo "  ✖ /usr/local/bin/rivolt-build is missing" | tee -a "$LOG"; exit 1; }
/usr/local/bin/rivolt-build "$RIVOLT_REF"
mv /opt/rivolt/rivolt.new /opt/rivolt/rivolt
mv /opt/rivolt/rivolt.new.version /opt/rivolt/VERSION

step "Starting service"
systemctl daemon-reload
systemctl enable --now rivolt >/dev/null 2>&1
ok=0
for i in $(seq 1 120); do
  if curl -fsS -o /dev/null "http://127.0.0.1:${PORT}/healthz"; then ok=1; break; fi
  systemctl is-failed --quiet rivolt && break
  sleep 2
done
if [[ $ok != 1 ]]; then
  echo "  ✖ Rivolt did not become healthy. Last service log lines:" | tee -a "$LOG"
  journalctl -u rivolt -n 40 --no-pager | tee -a "$LOG"
  exit 1
fi

step "Configuring SSH"
if [[ $SSH_ON == yes ]]; then
  echo "PermitRootLogin $([[ $SSH_PW == yes ]] && echo yes || echo prohibit-password)" >/etc/ssh/sshd_config.d/10-rivolt-root.conf
  systemctl enable --now ssh >/dev/null 2>&1 || true
  systemctl restart ssh >/dev/null 2>&1 || true
else
  systemctl disable --now ssh ssh.socket >/dev/null 2>&1 || true
fi

step "Cleaning up"
q apt-get -y autoremove
q apt-get -y clean
echo "  ✔ Rivolt $(cat /opt/rivolt/VERSION) installed"
EOF
}

push_and_install() {
  msg_info "Copying configuration into the container"
  pct exec "$CTID" -- mkdir -p /etc/rivolt
  pct push "$CTID" "$WORK/build.conf"       /etc/rivolt/build.conf       --perms 0600
  pct push "$CTID" "$WORK/rivolt.env"       /etc/rivolt/rivolt.env       --perms 0640
  pct push "$CTID" "$WORK/rivolt.service"   /etc/systemd/system/rivolt.service --perms 0644
  pct push "$CTID" "$WORK/rivolt-build"     /usr/local/bin/rivolt-build  --perms 0755
  pct push "$CTID" "$WORK/update-rivolt"    /usr/local/bin/update-rivolt --perms 0755
  pct push "$CTID" "$WORK/rivolt-install.sh" /root/rivolt-install.sh     --perms 0700
  rm -rf "$WORK"
  msg_ok "Copied configuration"

  echo -e " ${YW}➜${CL} Installing ${APP} inside CT ${CTID}:"
  pct exec "$CTID" -- env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin bash /root/rivolt-install.sh
  pct exec "$CTID" -- rm -f /root/rivolt-install.sh
  RIVOLT_VERSION=$(pct exec "$CTID" -- cat /opt/rivolt/VERSION)
  msg_ok "Installed ${APP} ${RIVOLT_VERSION}"
}

write_notes() {
  local signin="${AUTH}"
  [[ $AUTH != bypass ]] && signin="${OIDC_DISPLAY} - callback: ${BASE_URL}/api/auth/oidc/${OIDC_NAME}/callback"
  pct set "$CTID" --description "$(cat <<EOF
# ${APP} ${RIVOLT_VERSION}

Open-source Rivian tracker - [github.com/apohor/rivolt](https://github.com/apohor/rivolt)

- **URL:** [${BASE_URL}](${BASE_URL})  (listens on ${CT_IP}:${PORT})
- **Sign-in:** ${signin}
- **Config:** \`/etc/rivolt/rivolt.env\` then \`systemctl restart rivolt\`
- **Update:** \`update-rivolt\` inside the container
- **Logs:** \`journalctl -u rivolt -f\`
- **DB backups:** \`/var/backups/rivolt\` (taken by update-rivolt)
EOF
)" >/dev/null
}

# =============================================================================
#  Main
# =============================================================================
header_info
preflight
wt_yesno "${APP} LXC" "This will create a new Debian 13 LXC container and build ${APP} from source inside it.\n\nYou'll be asked for every setting; defaults are pre-filled.\n\nProceed?" 12 || cancel
ask_container
ask_rivolt
confirm
header_info
create_container
write_files
push_and_install
write_notes

echo
msg_ok "Completed successfully!"
echo -e " ${INFO} ${YW}${APP} is reachable at:${CL} ${BGN}${BASE_URL}${CL}"
[[ $URL_MODE == custom ]] && echo -e "    Point your reverse proxy at ${BGN}http://${CT_IP}:${PORT}${CL}"
if [[ $AUTH != bypass ]]; then
  echo -e " ${INFO} ${YW}Register this redirect/callback URL with ${OIDC_DISPLAY}:${CL}"
  echo -e "    ${BGN}${BASE_URL}/api/auth/oidc/${OIDC_NAME}/callback${CL}"
fi
[[ ${ROOTPW_GEN:-0} == 1 ]] && echo -e " ${INFO} ${YW}Generated container root password:${CL} ${ROOTPW}  (change it with 'passwd')"
echo -e " ${INFO} ${YW}Back up${CL} /etc/rivolt/rivolt.env - RIVOLT_KEK encrypts the stored Rivian credentials."
echo -e " ${INFO} Update later with: ${GN}pct exec ${CTID} -- update-rivolt${CL}"
