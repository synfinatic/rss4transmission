FROM golang:1.26-alpine AS builder
RUN apk add --update build-base git && \
	mkdir /codde
COPY . /code/
RUN cd /code && make

FROM alpine:latest
COPY --from=builder  /code/dist/rss4transmission /usr/local/bin/ 
ENV POLL_SECONDS=300
ENV LOG_LEVEL="info"
ENV HISTORY_FILE=""
ENV HISTORY_LISTEN=""
ENV TORRENT_CACHE_DIR=""

ENTRYPOINT exec /usr/local/bin/rss4transmission watch --sleep $POLL_SECONDS \
    --log-level $LOG_LEVEL --config /mnt/config.yaml --seen-file /mnt/cache.json \
    ${HISTORY_FILE:+--history-file $HISTORY_FILE} \
    ${HISTORY_LISTEN:+--history-listen $HISTORY_LISTEN} \
    ${TORRENT_CACHE_DIR:+--torrent-cache-dir $TORRENT_CACHE_DIR}
