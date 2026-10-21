# Copy the global files of koodam profile push at each login, so a new push is seen without a
# pod restart. The script is quiet if the ConfigMap did not change, and it never fails.
if [ -x /usr/local/bin/koodam-globals ]; then
  /usr/local/bin/koodam-globals || true
fi
