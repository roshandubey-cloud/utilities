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
  version "0.17.1"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-apple-silicon.zip"
      # SHA256 is updated by the release workflow on every tag. Until the
      # first run lands a real value, use the placeholder — `brew install`
      # will refuse to install (as it should) and surface the mismatch.
      sha256 "a4e908427d92f086ba5065562a9d465288573076939965ad91bc8967943ba7e9"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-intel.zip"
      sha256 "fef78e8dbb110d532eaae4f4b3f35b07cde3be7e7707920da94d593697a513c0"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-arm64.zip"
      sha256 "a4eae4e6db382b0f29a03d266fd2879b3ce43c8c003f67636a70fbe31aecc483"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-amd64.zip"
      sha256 "bc46f9492a68c5651a35db76b235b5efdb0e92e987e3d74bc7342a9bea2efc7d"
    end
  end

  def install
    bin.install Dir["sftp-loadtest-*"].first => "sftp-loadtest"
  end

  test do
    assert_match "sftp-loadtest #{version}", shell_output("#{bin}/sftp-loadtest -version")
  end
end
