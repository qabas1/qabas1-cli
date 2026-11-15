#!/usr/bin/env bash
# Practical Examples for qabas Installer
# These are real-world usage patterns and troubleshooting scenarios

# =============================================================================
# SCENARIO 1: Simple Linux Installation (Most Common)
# =============================================================================
echo "=== SCENARIO 1: Simple Installation (Recommended) ==="
echo "# On Linux, the simplest approach:"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash"
echo ""
echo "# Why it works:"
echo "  ✓ Automatically detects your GitHub token from 'gh' CLI"
echo "  ✓ Downloads the latest qabas CLI"
echo "  ✓ Installs system-wide to /usr/bin/qabas"
echo "  ✓ Automatically saves credentials for future 'sudo qabas' commands"
echo ""
echo ""

# =============================================================================
# SCENARIO 2: Browser-Based Authentication (Most User-Friendly)
# =============================================================================
echo "=== SCENARIO 2: Browser-Based Login (No Token Copy/Paste) ==="
echo "# If you don't have GitHub CLI credentials cached:"
echo "sudo bash /tmp/install.sh --browser-auth --cli-only"
echo ""
echo "# Or when piping:"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --browser-auth --cli-only"
echo ""
echo "# What happens:"
echo "  1. Installer checks if GitHub CLI is installed"
echo "  2. Opens your default browser with GitHub device login"
echo "  3. You approve access in the browser"
echo "  4. Token is automatically retrieved and saved"
echo "  ✓ No manual copy/paste needed"
echo "  ✓ No tokens visible in command history"
echo ""
echo ""

# =============================================================================
# SCENARIO 3: Specific Version Installation
# =============================================================================
echo "=== SCENARIO 3: Install Specific QabasOne Product Version ==="
echo "# Install both CLI and a specific product release:"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --release 2.8.0"
echo ""
echo "# Or with browser auth:"
echo "sudo bash /tmp/install.sh --browser-auth --release 2.8.0"
echo ""
echo "# Install CLI only (for Windows or quick test):"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only"
echo ""
echo ""

# =============================================================================
# SCENARIO 4: Using Explicit Token from Environment
# =============================================================================
echo "=== SCENARIO 4: Explicit Token via Environment ==="
echo "# From GitHub CLI (recommended):"
echo "export GITHUB_TOKEN=\$(gh auth token)"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo -E bash"
echo ""
echo "# From personal access token:"
echo "export GITHUB_TOKEN='ghp_xxxxxxxxxxxx'"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo -E bash"
echo ""
echo "# Why use -E with sudo:"
echo "  ✓ Preserves GITHUB_TOKEN environment variable"
echo "  ✓ Without -E, sudo clears env (security feature)"
echo ""
echo ""

# =============================================================================
# SCENARIO 5: Using Token from File
# =============================================================================
echo "=== SCENARIO 5: Token from File (CI/CD) ==="
echo "# Best practice for automation:"
echo "bash install.sh --token-file ~/.config/qabas/github-token --cli-only"
echo ""
echo "# Or in a Docker/CI container:"
echo "echo \"\${GITHUB_TOKEN}\" > /tmp/token.txt"
echo "sudo bash install.sh --token-file /tmp/token.txt --cli-only"
echo "rm /tmp/token.txt  # Clean up sensitive data"
echo ""
echo "# Token file format:"
echo "  - Plain text, one line"
echo "  - No extra whitespace"
echo "  - Permissions: 0600 (readable by owner only)"
echo ""
echo ""

# =============================================================================
# SCENARIO 6: Upgrading Existing Installation
# =============================================================================
echo "=== SCENARIO 6: Upgrade Existing Installation ==="
echo "# Simply re-run the installer - it's idempotent:"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash"
echo ""
echo "# Why it's safe:"
echo "  ✓ Detects current version"
echo "  ✓ Skips download if already at target version"
echo "  ✓ Upgrades if newer version available"
echo "  ✓ Refuses downgrade unless --allow-downgrade is used"
echo ""
echo "# To force reinstall (useful for debugging):"
echo "curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --force --cli-only"
echo ""
echo ""

# =============================================================================
# SCENARIO 7: Windows Installation
# =============================================================================
echo "=== SCENARIO 7: Windows (PowerShell) ==="
echo "# In PowerShell 5.1+:"
echo "iwr -useb https://install.qabas.one/install.ps1 -OutFile install.ps1"
echo ".\\install.ps1 -CliOnly"
echo ""
echo "# With browser authentication:"
echo ".\\install.ps1 -BrowserAuth -CliOnly"
echo ""
echo "# With explicit token:"
echo "\$env:GITHUB_TOKEN = gh auth token"
echo ".\\install.ps1 -CliOnly"
echo ""
echo "# Note: Windows only supports CLI installation"
echo "# For product stack, use Linux/WSL2"
echo ""
echo ""

# =============================================================================
# SCENARIO 8: Development/Testing with Dev Channel
# =============================================================================
echo "=== SCENARIO 8: Install from Dev Channel (Testing) ==="
echo "# Rolling development release (updated on every push to dev branch):"
echo "export GITHUB_TOKEN=\$(gh auth token)"
echo "VERSION=dev curl -fsSL https://install.qabas.one/install.sh | sudo -E bash -s -- --cli-only --force"
echo ""
echo "# Why you might use this:"
echo "  • Test bleeding-edge features"
echo "  • Debug reported issues"
echo "  • Verify fixes before release"
echo ""
echo "# Version defaults to 'latest' stable release"
echo ""
echo ""

# =============================================================================
# SCENARIO 9: Help and Troubleshooting
# =============================================================================
echo "=== SCENARIO 9: Help and Troubleshooting ==="
echo "# Get help on available options:"
echo "bash install.sh --help"
echo ""
echo "# Verify your GitHub credentials:"
echo "qabas auth status"
echo "sudo qabas auth status"
echo ""
echo "# Check CLI version after install:"
echo "qabas version"
echo ""
echo "# If credentials are missing:"
echo "export GITHUB_TOKEN=\$(gh auth token)"
echo "sudo -E qabas auth login"
echo ""
echo ""

# =============================================================================
# SCENARIO 10: CI/CD Pipeline Integration
# =============================================================================
echo "=== SCENARIO 10: CI/CD Pipeline Integration ==="
echo "# In GitHub Actions:"
echo "- name: Install qabas CLI"
echo "  env:"
echo "    GITHUB_TOKEN: \${{ github.token }}"
echo "  run: |"
echo "    curl -fsSL https://install.qabas.one/install.sh | sudo -E bash -s -- --cli-only"
echo ""
echo "# In GitLab CI:"
echo "- name: install"
echo "  script:"
echo "    - export GITHUB_TOKEN=\${CI_JOB_TOKEN}"
echo "    - curl -fsSL https://install.qabas.one/install.sh | sudo -E bash -s -- --cli-only"
echo ""
echo "# Generic CI/CD:"
echo "script:"
echo "  - echo \${GITHUB_TOKEN} > /tmp/token.txt"
echo "  - chmod 600 /tmp/token.txt"
echo "  - sudo bash /tmp/install.sh --token-file /tmp/token.txt --cli-only"
echo "  - rm /tmp/token.txt"
echo ""
echo ""

# =============================================================================
# SCENARIO 11: Error Cases and Solutions
# =============================================================================
echo "=== SCENARIO 11: Error Cases and Solutions ==="
echo ""
echo "ERROR: 'unsupported OS'"
echo "  SOLUTION: Run on Linux or Windows (this is not macOS compatible)"
echo ""
echo "ERROR: 'run as root'"
echo "  SOLUTION: Use 'sudo bash install.sh' or 'sudo' with curl pipe"
echo ""
echo "ERROR: 'missing required command: curl'"
echo "  SOLUTION: Install curl: apt install curl (Debian/Ubuntu)"
echo ""
echo "ERROR: 'token file not readable'"
echo "  SOLUTION: Verify file exists and is readable:"
echo "    ls -l ~/.config/qabas/github-token"
echo ""
echo "ERROR: '--browser-auth requires the GitHub CLI'"
echo "  SOLUTION: Install GitHub CLI: https://cli.github.com/"
echo ""
echo "ERROR: 'GitHub still refused the request'"
echo "  SOLUTION: Check token has these scopes:"
echo "    - 'repo' (for private repo access)"
echo "    - 'read:packages' (for container images)"
echo "  SAML SSO users: Authorize at https://github.com/settings/tokens"
echo ""
echo ""

# =============================================================================
# SCENARIO 12: Credential Management
# =============================================================================
echo "=== SCENARIO 12: Managing Credentials ==="
echo "# View current credential status:"
echo "qabas auth status"
echo ""
echo "# Set up GitHub credentials:"
echo "export GITHUB_TOKEN=\$(gh auth token)"
echo "sudo -E qabas auth set github"
echo ""
echo "# Set up GHCR credentials (for private images):"
echo "export GITHUB_TOKEN=\$(gh auth token)"
echo "sudo -E qabas auth set ghcr --username myusername"
echo ""
echo "# Remove saved credentials:"
echo "sudo qabas auth unset github"
echo ""
echo "# Credentials are stored in:"
echo "  /etc/qabasone/github-token (system-wide, mode 0600)"
echo "  /etc/qabasone/ghcr-token"
echo "  /etc/qabasone/ghcr-username"
echo ""
echo ""

echo "=== Additional Resources ==="
echo "Documentation: https://github.com/qabas1/qabas1-cli/blob/main/install/IMPROVEMENTS.md"
echo "GitHub Issues: https://github.com/qabas1/qabas1-cli/issues"
echo "Security: Always verify tokens are stored securely and never logged"
echo ""
