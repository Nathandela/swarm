# EXAMPLE Homebrew cask formula (E13.3) — committed for reference only. This is
# NOT installed by this repo and is NOT the live tap: it is a copy of what
# `goreleaser release` (driven by .goreleaser.yaml's homebrew_casks: section)
# actually writes to the Nathandela/homebrew-swarm tap repo on a real tagged
# release, captured from a local `goreleaser release --snapshot --clean
# --skip=publish` dry run so it stays honest with the real generator output.
# goreleaser regenerates and pushes the real file itself — never hand-edit a
# copy in the tap repo.
#
# Note on the sha256 values below: a goreleaser build embeds a build
# timestamp (the {{.Date}} ldflag), so re-running the same snapshot command
# against identical source produces DIFFERENT archive checksums every time —
# this file's hashes will not match your own local rebuild, and that is
# expected, not drift to chase.
#
# Once the tap repo exists, install with:
#   brew install --cask Nathandela/swarm/swarm
cask "swarm" do
  version "0.0.0-SNAPSHOT-1a5ec97"

  on_macos do
    on_intel do
      sha256 "06e5f15886cc490b4ddd8d3f3ffefc509cf47a4aa7663174b2032d0f3acc31ea"
      url "https://github.com/Nathandela/swarm/releases/download/v0.0.0/swarm_#{version}_darwin_amd64.tar.gz"
    end
    on_arm do
      sha256 "1455a37f053aa6e702c018c380f5a4c74eaf51bea9cd674466887c33844cfa9a"
      url "https://github.com/Nathandela/swarm/releases/download/v0.0.0/swarm_#{version}_darwin_arm64.tar.gz"
    end
  end

  on_linux do
    on_intel do
      sha256 "87c32d802501d20b22bcb0c5885d0cebaaa6300049b22d2abf9ad0d8935fceca"
      url "https://github.com/Nathandela/swarm/releases/download/v0.0.0/swarm_#{version}_linux_amd64.tar.gz"
    end
    on_arm do
      sha256 "04f1ed66e89b8afe7f85b5eb1dfd30b36292b08098410f9c6eff87eb00c48fd8"
      url "https://github.com/Nathandela/swarm/releases/download/v0.0.0/swarm_#{version}_linux_arm64.tar.gz"
    end
  end

  name "swarm"
  desc "Every coding agent on your machine, in one keyboard-driven terminal view"
  homepage "https://github.com/Nathandela/swarm"

  livecheck do
    skip "Auto-generated on release."
  end

  binary "swarm"

  postflight do
    system_command "/usr/bin/xattr", args: ["-dr", "com.apple.quarantine", "#{staged_path}/swarm"]
  end

  # No zap stanza required

end
