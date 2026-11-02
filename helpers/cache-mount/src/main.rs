#![recursion_limit = "512"]
//! Restricted mount actuator. No credential values in argv, stdout or errors.
//! The master stores mount properties (including credentials) in its metadata;
//! Secret projection does not encrypt that upstream protocol/storage boundary.
use curvine_config::ClusterConf;
use curvine_fs_api::{FileSystem, Path, Reader};
use curvine_model::{AccessMode, MountOptions, Provider, WriteType};
use curvine_runtime::runtime::RpcRuntime;
use curvine_unified_fs::{UfsFileSystem, UnifiedFileSystem};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::{collections::HashMap, sync::Arc, time::Duration};

type Result<T> = std::result::Result<T, Box<dyn std::error::Error + Send + Sync>>;
const SETTINGS: &str = "/etc/curvine/cache.json";
const CONF: &str = "/etc/curvine/curvine-cluster.toml";
const SECRET: &str = "/etc/curvine-origin";
fn field<'a>(v: &'a Value, key: &str) -> Result<&'a str> {
    v[key].as_str().ok_or_else(|| "invalid config".into())
}
fn credential(name: &str) -> Result<String> {
    let value = std::fs::read_to_string(format!("{SECRET}/{name}"))?;
    let value = value.trim();
    if value.is_empty() || value.len() > 4096 {
        return Err("invalid credential".into());
    }
    Ok(value.to_string())
}
fn properties(c: &Value) -> Result<HashMap<String, String>> {
    let r = &c["readCache"];
    let mut p = HashMap::new();
    let (endpoint, region, access, secret) = match field(r, "backend")? {
        "S3" => (
            "s3.endpoint_url",
            "s3.region_name",
            "s3.credentials.access",
            "s3.credentials.secret",
        ),
        "OSS" => (
            "oss.endpoint",
            "oss.region",
            "oss.accessKeyId",
            "oss.accessKeySecret",
        ),
        _ => return Err("unsupported backend".into()),
    };
    p.insert(endpoint.into(), field(r, "endpoint")?.into());
    p.insert(region.into(), field(r, "region")?.into());
    p.insert(access.into(), credential("accessKey")?);
    p.insert(secret.into(), credential("secretKey")?);
    if field(r, "backend")? == "S3" {
        p.insert("s3.force.path.style".into(), "true".into());
    }
    p.insert(
        "opendal.read_concurrent".into(),
        r["readConcurrency"].to_string(),
    );
    p.insert("opendal.connect_timeout".into(), "3s".into());
    p.insert("opendal.read_timeout".into(), "10s".into());
    p.insert("opendal.retry_times".into(), "1".into());
    p.insert(
        "operator.cluster_uid".into(),
        field(c, "clusterUID")?.into(),
    );
    p.insert(
        "operator.config_hash".into(),
        field(c, "configHash")?.into(),
    );
    Ok(p)
}
async fn origin(c: &Value, p: HashMap<String, String>) -> Result<()> {
    let r = &c["readCache"];
    let probe = &r["probe"];
    let size = probe["sizeBytes"]
        .as_u64()
        .filter(|x| *x > 0 && *x <= 16 * 1024 * 1024)
        .ok_or("invalid probe size")? as usize;
    let source = field(r, "source")?.trim_end_matches('/');
    let file = Path::from_str(format!("{source}/{}", field(probe, "object")?))?;
    let ufs = UfsFileSystem::new(&file, p, Some(Provider::Opendal))?;
    let mut reader = ufs.open(&file).await?;
    if reader.len() != size as i64 {
        return Err("origin content mismatch".into());
    }
    let mut bytes = vec![0; size];
    let read = reader.read_full(&mut bytes).await?;
    reader.complete().await?;
    if read != size || format!("{:x}", Sha256::digest(&bytes)) != field(probe, "sha256")? {
        return Err("origin content mismatch".into());
    }
    Ok(())
}
async fn configure(
    c: &Value,
    fs: UnifiedFileSystem,
    p: HashMap<String, String>,
    check_only: bool,
) -> Result<()> {
    let r = &c["readCache"];
    if field(r, "consistency")? != "Immutable" {
        return Err("unsupported consistency".into());
    }
    let source_path = Path::from_str(field(r, "source")?)?;
    let source = source_path.full_path();
    let dest = field(c, "mountPath")?;
    let ttl = r["ttlSeconds"]
        .as_i64()
        .filter(|v| *v >= 60 && *v <= 2592000)
        .ok_or("invalid ttl")?
        * 1000;
    let opts = MountOptions::builder()
        .set_properties(p.clone())
        .write_type(WriteType::CacheMode)
        .access_mode(AccessMode::ReadOnly)
        .write_cache(false)
        .auto_cache(true)
        .provider(Provider::Opendal)
        .ttl_ms(ttl)
        .replicas(1)
        .read_verify_ufs(false)
        .build();
    let matches = |m: &curvine_model::MountInfo| {
        m.cv_path == dest
            && m.ufs_path == source
            && m.properties == p
            && m.is_read_only_cache_mode()
            && !m.write_cache
            && m.auto_cache
            && !m.read_verify_ufs
            && m.ttl_ms == ttl
            && m.replicas == Some(1)
            && m.provider == Some(Provider::Opendal)
    };
    let table = fs.get_mount_table().await?;
    // A dedicated read-cache cluster has exactly one managed mount. Unknown
    // mounts are conflicts, even if they happen to point to the same source.
    if !table.is_empty() {
        if table.len() != 1 || !matches(&table[0]) {
            return Err("mount ownership or configuration conflict".into());
        }
        return Ok(());
    }
    if check_only {
        return Err("mount missing".into());
    }
    origin(c, p.clone()).await?;
    // No --update, unmount, delete or overwrite. A racing creator is checked
    // after the request; lost replies can be retried without taking ownership.
    let result = fs
        .mount(&Path::from_str(source)?, &Path::from_str(dest)?, opts)
        .await;
    let table = fs.get_mount_table().await?;
    if table.len() != 1 || !matches(&table[0]) {
        return Err("mount verification failed".into());
    }
    // A matching postcondition is sufficient after an uncertain RPC response.
    let _ = result;
    Ok(())
}
fn run() -> Result<()> {
    let action = std::env::args().nth(1).ok_or("missing action")?;
    if !["configure", "check", "origin"].contains(&action.as_str()) {
        return Err("unsupported action".into());
    }
    let bytes = std::fs::read(SETTINGS)?;
    if bytes.len() > 65536 {
        return Err("config too large".into());
    }
    let c: Value = serde_json::from_slice(&bytes)?;
    let p = properties(&c)?;
    let mut conf = ClusterConf::from(CONF)?;
    conf.client.init()?;
    let rt = Arc::new(conf.client_rpc_conf().create_runtime());
    let fs = UnifiedFileSystem::with_rt(conf, rt.clone())?;
    rt.block_on(async {
        tokio::time::timeout(Duration::from_secs(25), async {
            if action == "origin" {
                origin(&c, p).await
            } else {
                configure(&c, fs, p, action == "check").await
            }
        })
        .await
        .map_err(|_| "operation timed out")?
    })
}
fn main() {
    // Deliberately do not install a logger or print SDK error strings: those
    // can contain mount properties. Emit only a fixed, machine-readable result.
    if run().is_ok() {
        println!("{{\"ok\":true}}");
    } else {
        eprintln!("cache operation failed");
        std::process::exit(1);
    }
}
