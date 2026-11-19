# Release image, built by GoReleaser from the binaries it already made (see .goreleaser.yaml).
# distroless: CA certificates and a non-root user, nothing else.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETPLATFORM
# Go finds the config folder through HOME, which distroless doesn't set.
ENV HOME=/home/nonroot
COPY $TARGETPLATFORM/closedvps /usr/bin/closedvps
ENTRYPOINT ["/usr/bin/closedvps"]
