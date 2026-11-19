# The gate image of the kind test of the evidence gate (test/e2e/gate_test.go). It has the Nookku
# release of the pin in internal/evidence/nookku.pin, and a small gate project. pip refuses a
# wheel with a different SHA-256. Nookku has no dependency.
ARG BASE=docker.io/library/python:3.13-alpine
FROM ${BASE}
ARG NOOKKU_VERSION
ARG NOOKKU_WHEEL_SHA256
RUN printf 'nookku==%s --hash=sha256:%s\n' "$NOOKKU_VERSION" "$NOOKKU_WHEEL_SHA256" > /tmp/nookku.txt && \
    pip install --no-cache-dir --no-deps --only-binary :all: --require-hashes -r /tmp/nookku.txt && \
    rm /tmp/nookku.txt
COPY project /project
