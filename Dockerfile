FROM golang:1.20-alpine as builder
RUN apk add --update build-base git && \
	mkdir /codde
COPY . /code/
RUN cd /code && make

FROM alpine:latest
COPY --from=builder  /code/dist/rss4transmission /usr/local/bin/ 
ENV POLL_SECONDS=60
ENV LOG_LEVEL="info"
ENTRYPOINT ["/usr/local/bin/rss4transmission", "watch", "--sleep=$POLL_SECONDS", "--log-level=$LOG_LEVEL"]
CMD ["--config=/mnt/config.yaml", "--seen-file=/mnt/cache.json"]
