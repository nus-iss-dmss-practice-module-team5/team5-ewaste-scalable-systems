# Pre-Commit Hook Setup & Usage Guide

This project uses [pre-commit](https://pre-commit.com/) to manage and enforce code hygiene, format standards, syntax correctness, and security policies before commits are recorded to the repository.

Pre-commit runs automatically on `git commit`, preventing bad commits from entering your branches and ensuring all code passes the [CI Quality Gate (`ci-gate.yml`)](.github/workflows/ci-gate.yml).

---

## 1. Prerequisites

Make sure the following runtimes are installed on your machine:
- **Python 3.9+** and `pip` (Required to run the `pre-commit` framework)
- **Go 1.22+** (Required for Go formatting and vet hooks)
- **Node.js 20+** and `npm` (Required for Prettier formatting)
- **Git**

---

## 2. Installation

### Option A: Install via `pip` (All Platforms - Windows, macOS, Linux)
```bash
pip install pre-commit
```

Verify installation:
```bash
pre-commit --version
```

### Option B: Alternative Package Managers
- **macOS (Homebrew)**: `brew install pre-commit`
- **Windows (Winget)**: `winget install pre-commit.pre-commit`
- **Windows (Chocolatey)**: `choco install pre-commit`
- **Arch Linux**: `pacman -S pre-commit`

---

## 3. Install the Git Hook Scripts

After installing the `pre-commit` binary, install the git hook scripts into your local `.git/hooks` folder. Run this command once in the root of the repository:

```bash
pre-commit install
```

You should see output similar to:
```text
pre-commit installed at .git/hooks/pre-commit
```

From now on, every time you run `git commit`, the hooks configured in [`.pre-commit-config.yaml`](.pre-commit-config.yaml) will run automatically against your staged files.

---

## 4. How to Run Pre-Commit

### Automatic Execution (On Every Commit)
Whenever you make changes and run:
```bash
git add <files>
git commit -m "your commit message"
```
Pre-commit will automatically inspect your staged files.

### Manual Execution Across All Files
To run all hooks manually against every file in the repository (ideal before submitting a pull request):
```bash
pre-commit run --all-files
```

### Run a Single Hook
To run only a specific hook (e.g., Go formatting or Go vet):
```bash
# Run only go-fmt
pre-commit run go-fmt --all-files

# Run only go-vet
pre-commit run go-vet --all-files

# Run only prettier
pre-commit run prettier --all-files
```

---

## 5. Understanding Hook Behaviors & Output

### When `go-fmt` or `prettier` Modifies Files
Formatters (`go-fmt`, `prettier`, `trailing-whitespace`, `end-of-file-fixer`) will reformat unformatted files on disk.

When a formatter changes a file, pre-commit intentionally halts the commit and reports:
```text
go-fmt...................................................................Failed
- hook id: go-fmt
- files were modified by this hook
```
**This is expected behavior.** The hook has formatted the files for you. To complete your commit:
1. Inspect the changes (`git status` or `git diff`).
2. Stage the formatted changes:
   ```bash
   git add -u
   ```
3. Re-run your commit:
   ```bash
   git commit -m "your commit message"
   ```
   The hook will now report `Passed`.

---

## 6. Configured Hooks Overview

The hooks configured in [`.pre-commit-config.yaml`](.pre-commit-config.yaml) include:

| Hook ID | Category | Description |
| :--- | :--- | :--- |
| `trailing-whitespace` | Repository Hygiene | Trims superfluous trailing whitespace |
| `end-of-file-fixer` | Repository Hygiene | Ensures files end with a newline |
| `check-yaml` | Syntax Validation | Validates YAML syntax |
| `check-json` | Syntax Validation | Validates JSON syntax |
| `check-added-large-files` | Hygiene | Blocks accidental commits of large binaries (>1MB) |
| `check-merge-conflict` | Hygiene | Detects unmerged merge conflict markers |
| `detect-private-key` | Security | Prevents committing unencrypted private keys |
| `go-fmt` | Backend (Go) | Formats Go backend code in `src/backend/` |
| `go-vet` | Backend (Go) | Examines Go code in `src/backend/` for suspicious constructs |
| `prettier` | Frontend (UI/Docs) | Formats JavaScript, TypeScript, CSS, JSON, and Markdown in `src/frontend/` and `docs/` |

---

## 7. Useful Pre-Commit Commands

- **Update hook versions**:
  ```bash
  pre-commit autoupdate
  ```
- **Bypass hooks in an emergency**:
  ```bash
  git commit -m "urgent hotfix" --no-verify
  ```
- **Uninstall pre-commit hooks from git**:
  ```bash
  pre-commit uninstall
  ```

For more documentation and custom hook options, visit the official [pre-commit documentation](https://pre-commit.com/).
