# EXAMPLE Homebrew cask formula (E13.3) — committed for reference only. This is
# NOT installed by this repo and is NOT the live tap: it is a copy of what
# `goreleaser release` (driven by .goreleaser.yaml's homebrew_casks: section)
# actually writes to the Nathandela/homebrew-swarm tap repo on a real tagged
# release, captured from a local `goreleaser release --snapshot --clean
# --skip=publish` dry run so it stays honest with the real generator output.
# goreleaser regenerates and pushes the real file itself — never hand-edit a
# copy in the tap repo.
#
# macOS is served by a SINGLE universal (fat) Mach-O artifact (darwin_all): the
# .goreleaser.yaml universal_binaries: stanza lipos the arm64 and x86_64 builds
# into one binary that runs natively on both Apple Silicon and Intel, so the
# on_macos block has one url with no on_intel/on_arm split. Linux keeps its
# per-arch artifacts.
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
  version "0.3.0-SNAPSHOT-d52057e"

  on_macos do
    sha256 "4e22c595bb83064b5cc813cf73aced00f231dd2c462d0389471cd685d5753ec7"
    url "https://github.com/Nathandela/swarm/releases/download/v0.3.0/swarm_#{version}_darwin_all.tar.gz"
  end

  on_linux do
    on_intel do
      sha256 "fd73abeae0649260a7255b7f1a65ec1db16de89e4cbcb6abf26e101b8a1af540"
      url "https://github.com/Nathandela/swarm/releases/download/v0.3.0/swarm_#{version}_linux_amd64.tar.gz"
    end
    on_arm do
      sha256 "c62b96bca92633181386cd59e60da0ecf247dcea2668948f47deea3d41a022df"
      url "https://github.com/Nathandela/swarm/releases/download/v0.3.0/swarm_#{version}_linux_arm64.tar.gz"
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
