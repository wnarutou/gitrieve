FROM alpine:3.20.2 as alpine

RUN apk add --no-cache \
    ca-certificates \
    tzdata

# minimal image
FROM scratch
COPY --from=alpine \
    /etc/ssl/certs/ca-certificates.crt \
    /etc/ssl/certs/ca-certificates.crt
COPY --from=alpine \
    /usr/share/zoneinfo \
    /usr/share/zoneinfo

COPY gitrieve /

ENTRYPOINT ["/gitrieve"]
CMD ["run"]