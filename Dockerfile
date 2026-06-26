FROM golang:1.26-alpine AS builder
RUN apk add --update build-base git && \
	mkdir /codde
COPY . /code/
RUN cd /code && make

FROM alpine:latest
COPY --from=builder  /code/dist/rss4transmission /usr/local/bin/ 
ENV POLL_SECONDS=300
ENV LOG_LEVEL="info"
ENV HISTORY_PORT=0

ENTRYPOINT exec /usr/local/bin/rss4transmission watch --sleep $POLL_SECONDS \
    --log-level $LOG_LEVEL --config /mnt/config.yaml --seen-file /mnt/cache.json \
    --history-port $HISTORY_PORT
