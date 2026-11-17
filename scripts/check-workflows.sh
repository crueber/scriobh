#!/usr/bin/env bash
# Parse every workflow file with a real YAML parser and check the few keys a
# run cannot start without. An unquoted ": " in a step name once made the whole
# of ci.yml invalid YAML, and every run then failed in 0 s before its first step.
#
# Uses ruby (Psych) when present, else python3 with PyYAML. With neither it
# prints SKIPPED and exits 0, unless WORKFLOWS_CHECK_STRICT=1 (the workflow sets
# it), which makes "no parser" a failure.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

shopt -s nullglob
# The development repository keeps its workflows in .forgejo/workflows and the templates of the GitHub copy
# in scripts/publish; the published copy has only .github/workflows. Whichever exist are checked.
files=(.forgejo/workflows/*.yml .forgejo/workflows/*.yaml .github/workflows/*.yml .github/workflows/*.yaml scripts/publish/*.yml)
[ "${#files[@]}" -gt 0 ] || { echo "workflows-check: no workflow files under .forgejo/workflows, .github/workflows or scripts/publish" >&2; exit 1; }

if command -v ruby >/dev/null 2>&1; then
  ruby -ryaml -e '
    bad = 0
    ARGV.each do |f|
      begin
        doc = YAML.safe_load(File.read(f), aliases: true)
      rescue StandardError => e
        warn "#{f}: not valid YAML: #{e.message}"; bad += 1; next
      end
      jobs = doc.is_a?(Hash) ? doc["jobs"] : nil
      unless jobs.is_a?(Hash) && !jobs.empty?
        warn "#{f}: no jobs"; bad += 1; next
      end
      jobs.each do |name, job|
        %w[runs-on timeout-minutes steps].each do |k|
          (warn "#{f}: job #{name} has no #{k}"; bad += 1) unless job.is_a?(Hash) && job.key?(k)
        end
      end
      puts "#{f}: ok (#{jobs.keys.join(", ")})"
    end
    exit(bad.zero? ? 0 : 1)
  ' "${files[@]}"
elif command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
  python3 - "${files[@]}" <<'PY'
import sys, yaml
bad = 0
for f in sys.argv[1:]:
    try:
        with open(f, encoding="utf-8") as fh:
            doc = yaml.safe_load(fh)
    except yaml.YAMLError as e:
        print(f"{f}: not valid YAML: {e}", file=sys.stderr); bad += 1; continue
    jobs = doc.get("jobs") if isinstance(doc, dict) else None
    if not isinstance(jobs, dict) or not jobs:
        print(f"{f}: no jobs", file=sys.stderr); bad += 1; continue
    for name, job in jobs.items():
        for k in ("runs-on", "timeout-minutes", "steps"):
            if not isinstance(job, dict) or k not in job:
                print(f"{f}: job {name} has no {k}", file=sys.stderr); bad += 1
    print(f"{f}: ok ({', '.join(jobs)})")
sys.exit(1 if bad else 0)
PY
else
  if [ "${WORKFLOWS_CHECK_STRICT:-}" = 1 ]; then
    echo "workflows-check: FAILED: neither ruby nor python3 with PyYAML is available to parse the workflows" >&2
    exit 1
  fi
  echo "workflows-check: SKIPPED: neither ruby nor python3 with PyYAML is available; the workflow files were NOT parsed" >&2
fi
