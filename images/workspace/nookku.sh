# Export the Nookku test folder in a login shell (#83). The controller mounts the empty folder
# /etc/koodam/nookku only if a harness of the Workspace has the nookku plugin. The start script
# then makes the test folder on the home PVC, so the tests stay after the pod stops.
# If the user already exported NOOKKU_TESTS_DIR, this file does not change it.
if [ -d /etc/koodam/nookku ] && [ -z "${NOOKKU_TESTS_DIR:-}" ]; then
  export NOOKKU_TESTS_DIR=/home/dev/nookku-tests
fi
