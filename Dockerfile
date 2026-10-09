FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY lang/ lang/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/collab-server . && mkdir -p /out/data

FROM scratch
COPY --from=build /out/collab-server /collab-server
COPY --from=build --chown=65534:65534 /out/data /data
USER 65534:65534
ENV COLLAB_DATA_DIR=/data
ENV COLLAB_DOCKER=1
VOLUME /data
EXPOSE 7878
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/collab-server", "-healthcheck"]
ENTRYPOINT ["/collab-server"]
