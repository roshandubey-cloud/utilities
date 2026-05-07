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
  version "0.19.28"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-apple-silicon.zip"
      # SHA256 is updated by the release workflow on every tag. Until the
      # first run lands a real value, use the placeholder — `brew install`
      # will refuse to install (as it should) and surface the mismatch.
      sha256 "350a5bc40fa9dd450496fdee8df664b4c53be44336caef5270e7713d3f5d6822"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-mac-intel.zip"
      sha256 "f59756dd7ae4ff12ec287681fd02efb55423dde771026d242b848bd4ed8888be"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-arm64.zip"
      sha256 "b141a97c01188ff8d0038bf3099b8e8f35c2bebee7000e134621b1ff90024bc7"
    end
    on_intel do
      url "https://github.com/roshandubey-cloud/utilities/releases/download/v#{version}/sftp-loadtest-webui-v#{version}-linux-amd64.zip"
      sha256 "bafdbde69a64969236f4fe8ffd0a718f3a0659ef6a90b5c4840fd62be4d942fb"
    end
  end

  def install
    bin.install Dir["sftp-loadtest-*"].first => "sftp-loadtest"
  end

  test do
    assert_match "sftp-loadtest #{version}", shell_output("#{bin}/sftp-loadtest -version")
  end
end
