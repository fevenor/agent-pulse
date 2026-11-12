// EXE 和托盘共享脉冲图形，在构建时生成多尺寸资源，无需提交生成的二进制图片。
pub fn pixels(size: usize, unread: bool) -> Vec<u8> {
    let points = [
        (3f32, 12f32),
        (7., 12.),
        (10., 5.),
        (14., 19.),
        (17., 12.),
        (21., 12.),
    ];
    let mut data = vec![0; size * size * 4];
    for y in 0..size {
        for x in 0..size {
            let p = (
                (x as f32 + 0.5) * 24. / size as f32,
                (y as f32 + 0.5) * 24. / size as f32,
            );
            let distance = points
                .windows(2)
                .map(|line| {
                    let (a, b) = (line[0], line[1]);
                    let (dx, dy) = (b.0 - a.0, b.1 - a.1);
                    let t =
                        (((p.0 - a.0) * dx + (p.1 - a.1) * dy) / (dx * dx + dy * dy)).clamp(0., 1.);
                    ((p.0 - a.0 - t * dx).powi(2) + (p.1 - a.1 - t * dy).powi(2)).sqrt()
                })
                .fold(f32::MAX, f32::min);
            let alpha = ((1.15 - distance) * size as f32 / 24. + 0.5).clamp(0., 1.);
            let dot = unread && (p.0 - 20.).powi(2) + (p.1 - 4.).powi(2) < 9.;
            let color = if dot {
                [224, 122, 30, 255]
            } else {
                [54, 125, 226, (alpha * 255.) as u8]
            };
            data[(y * size + x) * 4..(y * size + x) * 4 + 4].copy_from_slice(&color);
        }
    }
    data
}
