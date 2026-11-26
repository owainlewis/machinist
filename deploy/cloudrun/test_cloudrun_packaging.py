import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CLOUDRUN_DIR = Path(__file__).resolve().parent


class CloudRunPackagingTests(unittest.TestCase):
    def test_dockerignore_excludes_secrets_and_state_but_keeps_web_dist(self):
        content = (ROOT / ".dockerignore").read_text(encoding="utf-8")
        lines = {line.strip() for line in content.splitlines() if line.strip() and not line.strip().startswith("#")}
        for required in (".git", ".env", ".env.*", ".machinist", "bin", "*.db", "internal/controlplane/web/node_modules"):
            self.assertIn(required, lines, f".dockerignore missing {required}")
        self.assertNotIn("internal/controlplane/web/dist", lines)

    def test_dockerfile_is_lean_verified_and_non_root(self):
        text = (CLOUDRUN_DIR / "Dockerfile").read_text(encoding="utf-8")
        stages = re.split(r"(?m)^FROM ", text)[1:]
        self.assertEqual(len(stages), 2, "expected two-stage Dockerfile")
        builder, runtime = "FROM " + stages[0], "FROM " + stages[1]

        # Builder verifies gh checksum with pipefail enabled.
        self.assertIn("pipefail", builder)
        self.assertIn("sha256sum --check", builder)
        self.assertIn("CGO_ENABLED=0", builder)

        # Runtime stage has no curl, kubectl, or git, runs as non-root UID 1000,
        # keeps /srv/machinist owned by root:root (read-only to UID 1000),
        # and directs writable paths to /tmp.
        self.assertIn("ca-certificates", runtime)
        for forbidden in ("curl", "kubectl", "git ", "apt-get install -y --no-install-recommends ca-certificates git"):
            self.assertNotIn(forbidden, runtime)
        self.assertIn("install -d -o root -g root -m 0755 /srv/machinist", runtime)
        self.assertIn("USER 1000:1000", runtime)
        self.assertIn("HOME=/tmp/machinist-home", runtime)
        self.assertIn("GH_CONFIG_DIR=/tmp/gh", runtime)
        self.assertIn('ENTRYPOINT ["machinist"]', runtime)

    def test_example_dockerfile_and_config_have_no_secrets(self):
        example_df = (CLOUDRUN_DIR / "Dockerfile.example").read_text(encoding="utf-8")
        self.assertIn("FROM ghcr.io/owainlewis/machinist:", example_df)
        self.assertIn("COPY --chmod=0644 config.yaml /srv/machinist/config.yaml", example_df)
        self.assertIn("COPY --chmod=0755 factory/ /srv/machinist/factory/", example_df)

        example_cfg = (CLOUDRUN_DIR / "config.example.yaml").read_text(encoding="utf-8")
        self.assertIn("trusted_proxy_hops: 1", example_cfg)
        self.assertIn("private_key_file: /secrets/github-app.pem", example_cfg)
        for secret_key in ("client_secret:", "database:", "mch_", "ghp_"):
            self.assertNotIn(secret_key, example_cfg)

    def test_deploy_script_syntax_and_flags(self):
        script = CLOUDRUN_DIR / "deploy.sh"
        text = script.read_text(encoding="utf-8")
        self.assertIn("set -euo pipefail", text)
        for required_flag in (
            "--min-instances=1",
            "--max-instances=1",
            "--no-cpu-throttling",
            "--memory=512Mi",
            "--timeout=120",
            "--set-secrets=",
        ):
            self.assertIn(required_flag, text)
        proc = subprocess.run(["bash", "-n", str(script)], capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_release_workflow_publishes_sha_pinned_multiarch_image(self):
        workflow = (ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
        self.assertIn("publish-image:", workflow)
        self.assertIn("packages: write", workflow)
        self.assertIn("deploy/cloudrun/Dockerfile", workflow)
        self.assertIn("linux/amd64,linux/arm64", workflow)
        self.assertIn("VERSION=${{ env.RELEASE_VERSION }}", workflow)
        # Every docker/* action must be pinned to a full 40-char commit SHA.
        for match in re.finditer(r"uses:\s*(docker/[^\s]+)", workflow):
            ref = match.group(1)
            self.assertRegex(ref, r"^docker/[a-z0-9-]+@[0-9a-f]{40}$", f"unpinned action: {ref}")


if __name__ == "__main__":
    unittest.main()
