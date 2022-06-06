#![allow(unused)]

use clap::Parser;

/// Search for a pattern in a file and display the lines that contain it.
#[derive(Parser)]
struct Cli {
    /// The pattern to look for
    song_title: String,
    // The path to the file to read
    // #[clap(parse(from_os_str))]
    // path: std::path::PathBuf,
}

impl std::fmt::Display for Cli {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(
            f,
            "(song: {})",
            self.song_title,
            // self.path.display()
        )
    }
}

fn main() {
    let args = Cli::parse();
    println!("{}", args);
}
