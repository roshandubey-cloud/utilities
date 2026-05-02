# Homebrew formula for sftp-loadtest (webui SKU).
#
# This file lives in the main repo for two reasons:
#   1. It's auto-updated by .github/workflows/release.yml on every tag —
#      a release-time step rewrites the `version`, `url`s, and `sha256`s
#      to point at the freshly published GitHub release assets, then opens
#      a PR against `roshandubey-cloud/homebrew-utilities` (the actual tap
#      repo). Keeping the source-of-truth here means the formula moves in
#      lockstep with the binary it installs.
#   2. Users who don't want a tap can `brew install ./Formula/sftp-loadtest.rb`
#      directly from a clone — useful for air-gapped or fork installs.
#
# Tap-repo install (recommended):
#   brew tap roshandubey-cloud/utilities
#   brew install sftp-loadtest
#
# Direct install (no tap):
#   brew install --build-from-source \
#     https://raw.githubusercontent.com/roshandubey-cloud/utilities/main/Formula/sftp-loadtest.rb
class SftpLoadtest < Formula
  desc "Multi-protocol load testing tool for SFTP, FTP, and FTPS servers"
  homepage "https://github.com/roshandubey-cloud/utilities"
  version "0.13.11"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-apple-silicon.zip"
      # SHA256 is updated by the release workflow on every tag. Until the
      # first run lands a real value, use the placeholder — `brew install`
      # will refuse to install (as it should) and surface the mismatch.
      sha256 "f5a15eea1ee72832695db45a6add83c5ae3f389805489ccbeae481492efe9323"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-intel.zip"
      sha256 "d7d010f74207610651730aa52b18eee9093de485c7265fabac397a1c0c4462f8"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-arm64.zip"
      sha256 "2e5165b5040c5dc02bf878f7f3fba0fca37d7cb2d3b90b39c983e915339b4150"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-amd64.zip"
      sha256 "ffab4187cc53e9972b0ecfc1fbe7ec897f36366e63d27f6d073c6d1e032de4b1"
    end
  end

  def install
    bin.install Dir["sftp-loadtest-*"].first => "sftp-loadtest"
  end

  test do
    assert_match "sftp-loadtest #{version}", shell_output("#{bin}/sftp-loadtest -version")
  end
end
