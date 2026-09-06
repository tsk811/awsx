# AWSX

AWSX is a Go CLI for signing in with AWS IAM Identity Center, selecting an account and role, and exporting temporary AWS credentials to your shell. It uses the AWS SDK directly, so the AWS CLI is not required.

> **Warning:** AWSX stores its configuration, client credentials, and SSO tokens in plaintext at `~/.awsx/config.yaml`. Nothing in this file is encrypted.

## Install

Install Go 1.27, then run `go install github.com/tsk811/awsx@latest`.

Add these lines to `~/.zshrc`, then restart Zsh or reload the file:

```zsh
export PATH="$HOME/go/bin:$PATH"
eval "$(command awsx shell-init zsh)"
```

## Use

```sh
awsx init    # Configure your IAM Identity Center URL and SSO region
awsx region  # Choose the AWS service region for your shell
awsx login   # Sign in, select an account and role, and export credentials
```

## Supported platforms

AWSX officially supports macOS with Zsh and the standard commercial AWS partition.
