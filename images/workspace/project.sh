# Run koodam-project at each login in each project folder of the home. A project folder is a
# direct child folder of the home with AGENTS.md or .agents/skills. The glob skips the dot
# folders. koodam-project reads no credential, writes only warnings, and never fails.
if [ -x /usr/local/bin/koodam-project ] && [ -n "${HOME:-}" ]; then
  for koodam_project_d in "$HOME"/*/; do
    if [ -e "${koodam_project_d}AGENTS.md" ] || [ -d "${koodam_project_d}.agents/skills" ]; then
      /usr/local/bin/koodam-project "$koodam_project_d" || true
    fi
  done
  unset koodam_project_d
fi
