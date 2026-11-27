# The Workspace image of the kind test of the nookku plugin (test/e2e/workspace_test.go). It is
# the Workspace image of Koodam plus the python3 of the official Python image, because the nookku
# plugin needs python3 3.11 or later (docs/nookku-preset.md). Koodam does not publish this image.
# The 2 bases are Debian 13, so the python3 binary uses the C library of the Workspace image.
ARG BASE
ARG PYTHON=docker.io/library/python:3.13-slim-trixie
FROM ${PYTHON} AS python

FROM ${BASE}
COPY --from=python /usr/local/bin/python3.13 /usr/local/bin/python3
COPY --from=python /usr/local/lib/libpython3.13.so.1.0 /usr/local/lib/
COPY --from=python /usr/local/lib/python3.13 /usr/local/lib/python3.13
USER 0:0
RUN ldconfig && python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))'
USER 1000:1000
