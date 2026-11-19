# The Nookku data image of Koodam (#83). It has the pinned Nookku files and the copier, and no
# shell and no Python. The init container koodam-nookku-files copies the files into the pod.
# Build it with `make docker-build-nookku`, which reads the pin from internal/evidence/nookku.pin.
# Each publication of this image needs a separate approval of the maintainer.
ARG GO_IMAGE=docker.io/library/golang:1.26
ARG PYTHON_IMAGE=docker.io/library/python:3.13-slim-trixie

FROM ${GO_IMAGE} AS copier
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/nookku-copy cmd/nookku-copy
RUN CGO_ENABLED=0 go build -trimpath -o /nookku-copy ./cmd/nookku-copy

FROM ${PYTHON_IMAGE} AS fetch
ARG NOOKKU_VERSION
ARG NOOKKU_WHEEL_SHA256
ARG NOOKKU_COMMIT
COPY images/nookku/fetch.py images/nookku/plugin.sha256 /fetch/
RUN python3 -I /fetch/fetch.py "$NOOKKU_VERSION" "$NOOKKU_WHEEL_SHA256" "$NOOKKU_COMMIT" \
      /fetch/plugin.sha256 /out
COPY images/nookku/nookku /out/bin/nookku

FROM scratch
COPY --from=copier /nookku-copy /nookku-copy
COPY --from=fetch /out /nookku
USER 65532:65532
ENTRYPOINT ["/nookku-copy"]
