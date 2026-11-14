FROM rust:1-bookworm AS build
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY src/ src/
COPY lang/ lang/
RUN cargo build --locked --release && mkdir /out-data

FROM debian:bookworm-slim
COPY --from=build /src/target/release/collab-server /usr/local/bin/collab-server
COPY --from=build --chown=65534:65534 /out-data /data
USER 65534:65534
ENV COLLAB_DATA_DIR=/data COLLAB_DOCKER=1
VOLUME /data
EXPOSE 7878
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["collab-server", "-healthcheck"]
ENTRYPOINT ["collab-server"]
