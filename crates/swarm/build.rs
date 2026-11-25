use std::{env, fs, path::PathBuf};

fn main() {
    println!("cargo:rerun-if-env-changed=SWARM_BUILD_MEDIA_PINS");
    let pins = match env::var_os("SWARM_BUILD_MEDIA_PINS") {
        Some(path) => {
            println!("cargo:rerun-if-changed={}", path.to_string_lossy());
            fs::read(path).expect("read release media pins")
        }
        None => b"{}".to_vec(),
    };
    let output = PathBuf::from(env::var_os("OUT_DIR").expect("Cargo OUT_DIR"));
    fs::write(output.join("release-media.json"), pins).expect("write release media pins");
}
