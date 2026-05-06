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
  version "0.19.10"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-apple-silicon.zip"
      # SHA256 is updated by the release workflow on every tag. Until the
      # first run lands a real value, use the placeholder — `brew install`
      # will refuse to install (as it should) and surface the mismatch.
      sha256 "d7371520ab9f57c4fd787c6cfc4738d0dfea265989fb6814273ce6f8bb5a5324"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-intel.zip"
      sha256 "ae0a66928c9d685f408018302ab25bb019e20fda6c74bf7e060b1f5adf9dd29c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-arm64.zip"
      sha256 "f61ff063906078f7b16a9d2c155d372916a4139d5847c9cdd00a6950b8f4cc35"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-amd64.zip"
      sha256 "949913cbe2c5c055d0b4c2846caa864b724cee66a03ef404f982fde8d9bd63c9"
    end
  end

  def install
    bin.install Dir["sftp-loadtest-*"].first => "sftp-loadtest"
  end

  test do
    assert_match "sftp-loadtest #{version}", shell_output("#{bin}/sftp-loadtest -version")
  end
end
