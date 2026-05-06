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
  version "0.19.11"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-apple-silicon.zip"
      # SHA256 is updated by the release workflow on every tag. Until the
      # first run lands a real value, use the placeholder — `brew install`
      # will refuse to install (as it should) and surface the mismatch.
      sha256 "47c9c8e93c67ad34c07d2a7daba3e3dcc073029ad44de9f89dc5fcf44c5168d8"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-intel.zip"
      sha256 "341e66054360589e4981be67ca12a44c91f61ae5fd79d312e1a9d85b26499d04"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-arm64.zip"
      sha256 "ac9b24a6a0f265a16620f9c599f03a8077e99b77cad5438520c84789618a49e6"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-amd64.zip"
      sha256 "4a8c0ef93daef34348185a997d4c9c7214b704017eaa0e0a1d00bbf82c0101a1"
    end
  end

  def install
    bin.install Dir["sftp-loadtest-*"].first => "sftp-loadtest"
  end

  test do
    assert_match "sftp-loadtest #{version}", shell_output("#{bin}/sftp-loadtest -version")
  end
end
