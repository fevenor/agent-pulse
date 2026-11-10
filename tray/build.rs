use std::{env, fs, path::PathBuf};

fn main() {
    println!("cargo:rerun-if-env-changed=AGENT_PULSE_BRIDGE_BINARY");
    let output = PathBuf::from(env::var_os("OUT_DIR").unwrap()).join("bridge.bin");
    if let Some(path) = env::var_os("AGENT_PULSE_BRIDGE_BINARY") {
        println!("cargo:rerun-if-changed={}", PathBuf::from(&path).display());
        fs::copy(path, output).expect("无法内置本地接入程序");
    } else {
        fs::write(output, []).expect("无法建立开发构建占位资源");
    }
}
