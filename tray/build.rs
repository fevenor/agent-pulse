use std::{env, fs, path::PathBuf};
#[path = "src/artwork.rs"]
mod artwork;

fn main() {
    println!("cargo:rerun-if-changed=src/artwork.rs");
    if env::var("CARGO_CFG_TARGET_OS").as_deref() == Ok("windows") {
        let directory = PathBuf::from(env::var_os("OUT_DIR").unwrap());
        let icon = directory.join("agent-pulse.ico");
        fs::write(&icon, icon_bytes()).expect("无法生成程序图标");
        let resource = directory.join("icon.rc");
        fs::write(
            &resource,
            format!(
                "1 ICON \"{}\"\n",
                icon.display().to_string().replace('\\', "/")
            ),
        )
        .expect("无法生成图标资源声明");
        embed_resource::compile(&resource, embed_resource::NONE)
            .manifest_required()
            .expect("无法内置程序图标");
    }
    println!("cargo:rerun-if-env-changed=AGENT_PULSE_BRIDGE_BINARY");
    let output = PathBuf::from(env::var_os("OUT_DIR").unwrap()).join("bridge.bin");
    if let Some(path) = env::var_os("AGENT_PULSE_BRIDGE_BINARY") {
        println!("cargo:rerun-if-changed={}", PathBuf::from(&path).display());
        fs::copy(path, output).expect("无法内置本地接入程序");
    } else {
        fs::write(output, []).expect("无法建立开发构建占位资源");
    }
}

fn icon_bytes() -> Vec<u8> {
    let sizes = [16usize, 24, 32, 48, 64, 128, 256];
    let mut header = vec![0, 0, 1, 0, sizes.len() as u8, 0];
    let mut images = Vec::new();
    for size in sizes {
        let pixels = artwork::pixels(size, false);
        let mask_stride = size.div_ceil(32) * 4;
        let length = 40 + size * size * 4 + mask_stride * size;
        header.extend([size as u8, size as u8, 0, 0, 1, 0, 32, 0]);
        header.extend((length as u32).to_le_bytes());
        header.extend(((6 + 16 * sizes.len() + images.len()) as u32).to_le_bytes());
        images.extend(40u32.to_le_bytes());
        images.extend((size as i32).to_le_bytes());
        images.extend(((size * 2) as i32).to_le_bytes());
        images.extend([1, 0, 32, 0]);
        images.extend([0u8; 24]);
        for y in (0..size).rev() {
            for x in 0..size {
                let p = &pixels[(y * size + x) * 4..][..4];
                images.extend([p[2], p[1], p[0], p[3]]);
            }
        }
        images.resize(images.len() + mask_stride * size, 0);
    }
    header.extend(images);
    header
}
