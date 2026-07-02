#!/bin/bash
#
# Transmission "torrent done" hook.
#
# Set RSS4TRANSMISSION_URL to the base URL of the rss4transmission cancel/notify
# server (same host:port as --cancel-listen, e.g. http://localhost:8080).
# The server renders the notification using your configured templates and priority.
#
# If Cancel.HMACSecret is configured in rss4transmission, set CANCEL_HMAC_SECRET
# to the same value so the endpoint can authenticate the request.
#
# Example (Docker Compose):
#   environment:
#     - RSS4TRANSMISSION_URL=http://rss4transmission:8080
#     - CANCEL_HMAC_SECRET=<same value as Cancel.HMACSecret in config.yaml>
#
# Transmission passes torrent details via environment variables:
#   TR_TORRENT_NAME  - torrent name
#   TR_TORRENT_DIR   - download directory
#   TR_TORRENT_ID    - Transmission torrent ID (integer)

CURL_ARGS=()
if [ -n "${CANCEL_HMAC_SECRET}" ]; then
    CURL_ARGS+=("-H" "Authorization: Bearer ${CANCEL_HMAC_SECRET}")
fi

/usr/bin/curl -s -X POST \
    -H "Content-Type: application/json" \
    "${CURL_ARGS[@]}" \
    -d "{\"name\":\"${TR_TORRENT_NAME}\",\"dir\":\"${TR_TORRENT_DIR}\",\"id\":${TR_TORRENT_ID}}" \
    "${RSS4TRANSMISSION_URL}/notify-complete"
