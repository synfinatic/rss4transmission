#!/bin/bash

/usr/bin/curl -s \
    -H "Authorization: Bearer ${NTFY_TOKEN}" \
    -H "Title: Torrent Complete: ${TR_TORRENT_NAME}" \
    -H "Priority: default" \
    -d "File saved to ${TR_TORRENT_DIR} with ID ${TR_TORRENT_ID}" \
    "${NTFY_BASE_URL}/${NTFY_TOPIC}"
