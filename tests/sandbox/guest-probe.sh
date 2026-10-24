#!/usr/bin/env bash
# Synthetic, bounded guest probes only. No real credential or private-path reads.
set -uo pipefail

failed=0
report() {
  local test_id="$1" status="$2" detail="$3"
  printf '%s\t%s\t%s\n' "$test_id" "$status" "$detail"
  if [ "$status" = FAIL ]; then failed=$((failed + 1)); fi
}

report guest-kernel INFO "$(uname -sr)"
if [ -r /proc/sys/kernel/random/boot_id ]; then
  report guest-boot-id INFO "$(</proc/sys/kernel/random/boot_id)"
fi
report guest-uid INFO "$(id -u)"

if [ -n "${SSH_AUTH_SOCK:-}" ] && [ -S "${SSH_AUTH_SOCK}" ]; then
  report ssh-agent FAIL 'an SSH agent socket is present; forwarding requires review'
else
  report ssh-agent PASS 'no usable SSH_AUTH_SOCK exposed to this process'
fi

if [ -S /var/run/docker.sock ]; then
  report docker-socket INFO 'guest Docker socket exists; does not by itself imply host socket exposure'
else
  report docker-socket INFO 'no default guest Docker socket'
fi

if [ -n "${PULSAR_SYNTHETIC_CANARY_POSIX:-}" ]; then
  if [ -e "$PULSAR_SYNTHETIC_CANARY_POSIX" ]; then
    report host-canary FAIL 'synthetic host-only path unexpectedly visible'
  else
    report host-canary PASS 'specified synthetic host-only path is not visible'
  fi
else
  report host-canary SKIP 'controller did not supply the translated synthetic path'
fi

if command -v curl >/dev/null 2>&1; then
  if curl --silent --fail --max-time 8 --noproxy '*' https://example.com/ -o /dev/null; then
    report direct-internet FAIL 'direct outbound HTTPS succeeded'
  else
    report direct-internet INCONCLUSIVE 'direct outbound HTTPS failed; needs positive control and policy denial evidence'
  fi
  if curl --silent --fail --max-time 8 https://example.com/ -o /dev/null; then
    report proxied-internet FAIL 'unapproved external HTTPS succeeded'
  else
    report proxied-internet INCONCLUSIVE 'unapproved external HTTPS failed; needs positive control and policy denial evidence'
  fi
else
  report direct-internet SKIP 'curl not available in guest template'
  report proxied-internet SKIP 'curl not available in guest template'
fi

report note INFO 'network failures alone do not prove isolation: perform a positive control and inspect deny evidence'
report note INFO 'not a complete penetration test; cross-environment and lifecycle probes are controller-driven'
exit "$failed"
