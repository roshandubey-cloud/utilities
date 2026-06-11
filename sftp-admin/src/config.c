// Portable config loader. JSON file via stdio -> typed sa_config_t.
//
// Defaults are platform-aware: a Windows binary defaults to
// %APPDATA%\sftpadmin\* paths, macOS to ~/Library/Application Support/...,
// Linux/BSD to $XDG_DATA_HOME or ~/.local/share. This means a fresh
// build runs with sensible defaults on every platform without the user
// editing a config file first — important for the desktop-app use case.

#include "sftpadmin/config.h"
#include "sftpadmin/portable.h"

#include <cJSON.h>

#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>

#define SA_CFG_MAX_BYTES (1024 * 1024)
#define SA_PATH_CAP      1024

static char *xstrdup_or_null(const char *s) {
    if (!s) return NULL;
    size_t n = strlen(s) + 1;
    char *p = malloc(n);
    if (!p) return NULL;
    memcpy(p, s, n);
    return p;
}

// Builds "<data_dir><SEP><suffix>" into a fresh heap string.
static char *path_under_data(const char *data_dir, const char *suffix) {
    if (!data_dir || !suffix) return NULL;
    size_t a = strlen(data_dir), b = strlen(suffix), sep = strlen(SA_PATHSEP);
    char *out = malloc(a + sep + b + 1);
    if (!out) return NULL;
    memcpy(out, data_dir, a);
    memcpy(out + a, SA_PATHSEP, sep);
    memcpy(out + a + sep, suffix, b + 1);
    return out;
}

void sa_config_defaults(sa_config_t *out) {
    if (!out) return;
    memset(out, 0, sizeof(*out));

    char data[SA_PATH_CAP];
    (void)sa_default_data_dir(data, sizeof(data));

    out->db_path                       = path_under_data(data, "sftpadmin.db");
    out->hostkey_dir                   = path_under_data(data, "hostkeys");
    out->master_key_file               = path_under_data(data, "master.key");
    out->run_dir                       = path_under_data(data, "run");
    out->log_file                      = NULL;
    out->admin_bind_addr               = xstrdup_or_null("127.0.0.1");
    out->admin_port                    = 9443;
    out->admin_tls_cert                = path_under_data(data, "admin-cert.pem");
    out->admin_tls_key                 = path_under_data(data, "admin-key.pem");
    out->admin_generate_self_signed    = true;
    out->log_level                     = SA_LOG_INFO;
    out->log_to_syslog                 = false;
    out->argon2_ops                    = 3;
    out->argon2_mem_kb                 = 65536;
    out->default_max_sessions          = 100;
    out->default_max_sessions_per_user = 10;
    out->default_idle_timeout_s        = 600;
    out->default_auth_grace_s          = 30;
}

void sa_config_free(sa_config_t *cfg) {
    if (!cfg) return;
    free(cfg->db_path);          cfg->db_path = NULL;
    free(cfg->hostkey_dir);      cfg->hostkey_dir = NULL;
    free(cfg->master_key_file);  cfg->master_key_file = NULL;
    free(cfg->run_dir);          cfg->run_dir = NULL;
    free(cfg->log_file);         cfg->log_file = NULL;
    free(cfg->admin_bind_addr);  cfg->admin_bind_addr = NULL;
    free(cfg->admin_tls_cert);   cfg->admin_tls_cert = NULL;
    free(cfg->admin_tls_key);    cfg->admin_tls_key = NULL;
}

// ---------------------------------------------------------------------------
// Per-field setters. Each updates the destination only when the JSON node
// is present AND of the right type. Wrong-type values produce a warning
// and leave defaults untouched.
// ---------------------------------------------------------------------------
static void set_string(const cJSON *obj, const char *key, char **dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsString(v) || !v->valuestring) {
        sa_log_warn(subsys, "config field has wrong type; ignoring",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    char *new_value = xstrdup_or_null(v->valuestring);
    if (!new_value) {
        sa_log_error(subsys, "out of memory updating config field",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    free(*dst);
    *dst = new_value;
}

static void set_bool(const cJSON *obj, const char *key, bool *dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsBool(v)) {
        sa_log_warn(subsys, "config field expected bool; ignoring",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    *dst = cJSON_IsTrue(v) ? true : false;
}

static void set_u16(const cJSON *obj, const char *key, uint16_t *dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsNumber(v) || v->valuedouble < 1 || v->valuedouble > 65535) {
        sa_log_warn(subsys, "config field out of range for uint16; ignoring",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    *dst = (uint16_t)v->valuedouble;
}

static void set_u32(const cJSON *obj, const char *key, uint32_t *dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsNumber(v) || v->valuedouble < 0 || v->valuedouble > 4294967295.0) {
        sa_log_warn(subsys, "config field out of range for uint32; ignoring",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    *dst = (uint32_t)v->valuedouble;
}

static void set_u64(const cJSON *obj, const char *key, uint64_t *dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsNumber(v) || v->valuedouble < 0) {
        sa_log_warn(subsys, "config field expected non-negative number; ignoring",
            SA_LOG_KV("key", key), SA_LOG_END);
        return;
    }
    *dst = (uint64_t)v->valuedouble;
}

static void set_loglevel(const cJSON *obj, const char *key, sa_log_level_t *dst, const char *subsys) {
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!v) return;
    if (!cJSON_IsString(v) || !v->valuestring) {
        sa_log_warn(subsys, "log_level must be a string; ignoring", SA_LOG_END);
        return;
    }
    if      (!strcmp(v->valuestring, "debug")) *dst = SA_LOG_DEBUG;
    else if (!strcmp(v->valuestring, "info"))  *dst = SA_LOG_INFO;
    else if (!strcmp(v->valuestring, "warn"))  *dst = SA_LOG_WARN;
    else if (!strcmp(v->valuestring, "error")) *dst = SA_LOG_ERROR;
    else {
        sa_log_warn(subsys, "unknown log_level; keeping default",
            SA_LOG_KV("value", v->valuestring), SA_LOG_END);
    }
}

static void warn_unknown_keys(const cJSON *obj, const char *section,
                              const char *const *known, size_t known_n) {
    for (const cJSON *child = obj ? obj->child : NULL; child; child = child->next) {
        if (!child->string) continue;
        if (child->string[0] == '_') continue;  // _comment convention
        bool ok = false;
        for (size_t i = 0; i < known_n; i++) {
            if (!strcmp(child->string, known[i])) { ok = true; break; }
        }
        if (!ok) {
            sa_log_warn("config", "unknown config key (forward-compat ignored)",
                SA_LOG_KV("section", section),
                SA_LOG_KV("key", child->string),
                SA_LOG_END);
        }
    }
}

// Path absolutness check. POSIX absolute paths start with '/', Windows
// absolute paths start with a drive letter + colon + separator OR with
// "\\" (UNC). UNC paths get a pass-through.
static bool is_absolute_path(const char *p) {
    if (!p || !*p) return false;
#ifdef _WIN32
    // C:\... or C:/... — drive letter + colon + slash.
    if (((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
        && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) return true;
    if (p[0] == '\\' && p[1] == '\\') return true;  // UNC
    return false;
#else
    return p[0] == '/';
#endif
}

sa_err_t sa_config_validate(const sa_config_t *cfg) {
    if (!cfg) return SA_ERR_INVAL;

    const struct { const char *name; const char *val; } req_abs[] = {
        {"db_path",          cfg->db_path},
        {"hostkey_dir",      cfg->hostkey_dir},
        {"master_key_file",  cfg->master_key_file},
        {"run_dir",          cfg->run_dir},
        {"admin_tls_cert",   cfg->admin_tls_cert},
        {"admin_tls_key",    cfg->admin_tls_key},
    };
    for (size_t i = 0; i < sizeof(req_abs) / sizeof(req_abs[0]); i++) {
        if (!req_abs[i].val || !*req_abs[i].val) {
            sa_log_error("config", "required path missing",
                SA_LOG_KV("key", req_abs[i].name), SA_LOG_END);
            return SA_ERR_SCHEMA;
        }
        if (!is_absolute_path(req_abs[i].val)) {
            sa_log_error("config", "path must be absolute",
                SA_LOG_KV("key", req_abs[i].name),
                SA_LOG_KV("value", req_abs[i].val),
                SA_LOG_END);
            return SA_ERR_SCHEMA;
        }
    }

    if (!cfg->admin_bind_addr || !*cfg->admin_bind_addr) {
        sa_log_error("config", "admin_bind_addr must be set", SA_LOG_END);
        return SA_ERR_SCHEMA;
    }
    if (cfg->admin_port == 0) {
        sa_log_error("config", "admin_port must be > 0", SA_LOG_END);
        return SA_ERR_SCHEMA;
    }

    if (cfg->argon2_ops < 1 || cfg->argon2_ops > 32) {
        sa_log_error("config", "argon2_ops must be in [1, 32]",
            SA_LOG_KV_INT("value", (long long)cfg->argon2_ops), SA_LOG_END);
        return SA_ERR_SCHEMA;
    }
    if (cfg->argon2_mem_kb < 8192 || cfg->argon2_mem_kb > (1ULL << 22)) {
        sa_log_error("config", "argon2_mem_kb must be in [8192, 4194304]",
            SA_LOG_KV_INT("value", (long long)cfg->argon2_mem_kb), SA_LOG_END);
        return SA_ERR_SCHEMA;
    }

    if (cfg->default_max_sessions < 1) {
        sa_log_error("config", "default_max_sessions must be >= 1", SA_LOG_END);
        return SA_ERR_SCHEMA;
    }
    if (cfg->default_max_sessions_per_user < 1) {
        sa_log_error("config", "default_max_sessions_per_user must be >= 1", SA_LOG_END);
        return SA_ERR_SCHEMA;
    }

    return SA_OK;
}

sa_err_t sa_config_load_buf(const char *json, size_t len, sa_config_t *out) {
    if (!json || !out)       return SA_ERR_INVAL;
    if (len == 0)            return SA_ERR_PARSE;
    if (len > SA_CFG_MAX_BYTES) return SA_ERR_TOOBIG;

    sa_config_defaults(out);

    cJSON *root = cJSON_ParseWithLength(json, len);
    if (!root) {
        const char *ep = cJSON_GetErrorPtr();
        sa_log_error("config", "JSON parse failed",
            SA_LOG_KV("near", ep ? ep : ""), SA_LOG_END);
        return SA_ERR_PARSE;
    }
    if (!cJSON_IsObject(root)) {
        cJSON_Delete(root);
        sa_log_error("config", "top-level JSON must be an object", SA_LOG_END);
        return SA_ERR_SCHEMA;
    }

    cJSON *paths = cJSON_GetObjectItemCaseSensitive(root, "paths");
    if (paths && cJSON_IsObject(paths)) {
        static const char *known[] = {
            "db_path","hostkey_dir","master_key_file","run_dir","log_file",
        };
        set_string(paths, "db_path",         &out->db_path,         "config");
        set_string(paths, "hostkey_dir",     &out->hostkey_dir,     "config");
        set_string(paths, "master_key_file", &out->master_key_file, "config");
        set_string(paths, "run_dir",         &out->run_dir,         "config");
        set_string(paths, "log_file",        &out->log_file,        "config");
        warn_unknown_keys(paths, "paths", known, sizeof(known) / sizeof(known[0]));
    }

    cJSON *admin = cJSON_GetObjectItemCaseSensitive(root, "admin");
    if (admin && cJSON_IsObject(admin)) {
        static const char *known[] = {
            "bind_addr","port","tls_cert","tls_key","generate_self_signed",
        };
        set_string(admin, "bind_addr",            &out->admin_bind_addr,            "config");
        set_u16   (admin, "port",                 &out->admin_port,                 "config");
        set_string(admin, "tls_cert",             &out->admin_tls_cert,             "config");
        set_string(admin, "tls_key",              &out->admin_tls_key,              "config");
        set_bool  (admin, "generate_self_signed", &out->admin_generate_self_signed, "config");
        warn_unknown_keys(admin, "admin", known, sizeof(known) / sizeof(known[0]));
    }

    cJSON *logging = cJSON_GetObjectItemCaseSensitive(root, "logging");
    if (logging && cJSON_IsObject(logging)) {
        static const char *known[] = { "level", "syslog" };
        set_loglevel(logging, "level",  &out->log_level,     "config");
        set_bool    (logging, "syslog", &out->log_to_syslog, "config");
        warn_unknown_keys(logging, "logging", known, sizeof(known) / sizeof(known[0]));
    }

    cJSON *sec = cJSON_GetObjectItemCaseSensitive(root, "security");
    if (sec && cJSON_IsObject(sec)) {
        static const char *known[] = { "argon2_ops", "argon2_mem_kb" };
        set_u64(sec, "argon2_ops",    &out->argon2_ops,    "config");
        set_u64(sec, "argon2_mem_kb", &out->argon2_mem_kb, "config");
        warn_unknown_keys(sec, "security", known, sizeof(known) / sizeof(known[0]));
    }

    cJSON *sftp = cJSON_GetObjectItemCaseSensitive(root, "sftp");
    if (sftp && cJSON_IsObject(sftp)) {
        static const char *known[] = {
            "default_max_sessions","default_max_sessions_per_user",
            "default_idle_timeout_s","default_auth_grace_s",
        };
        set_u32(sftp, "default_max_sessions",          &out->default_max_sessions,          "config");
        set_u32(sftp, "default_max_sessions_per_user", &out->default_max_sessions_per_user, "config");
        set_u32(sftp, "default_idle_timeout_s",        &out->default_idle_timeout_s,        "config");
        set_u32(sftp, "default_auth_grace_s",          &out->default_auth_grace_s,          "config");
        warn_unknown_keys(sftp, "sftp", known, sizeof(known) / sizeof(known[0]));
    }

    static const char *known_top[] = { "paths","admin","logging","security","sftp" };
    warn_unknown_keys(root, "(root)", known_top, sizeof(known_top) / sizeof(known_top[0]));

    cJSON_Delete(root);

    sa_err_t v = sa_config_validate(out);
    if (v != SA_OK) {
        sa_config_free(out);
        return v;
    }
    return SA_OK;
}

sa_err_t sa_config_load(const char *path, sa_config_t *out) {
    if (!path || !out) return SA_ERR_INVAL;

    // Portable I/O via stdio. "rb" so Windows doesn't translate CRLF.
    FILE *f = fopen(path, "rb");
    if (!f) {
        sa_err_t e = sa_err_from_errno(errno);
        sa_log_err("config", "could not open config file", e,
            SA_LOG_KV("path", path), SA_LOG_END);
        return e;
    }
    // Stat-via-stdio: seek to end, ftell, seek back.
    if (fseek(f, 0, SEEK_END) != 0) {
        sa_err_t e = sa_err_from_errno(errno);
        (void)fclose(f);
        return e;
    }
    long pos = ftell(f);
    if (pos < 0) {
        sa_err_t e = sa_err_from_errno(errno);
        (void)fclose(f);
        return e;
    }
    if (pos == 0) {
        (void)fclose(f);
        sa_log_error("config", "config file is empty",
            SA_LOG_KV("path", path), SA_LOG_END);
        return SA_ERR_PARSE;
    }
    if ((unsigned long)pos > SA_CFG_MAX_BYTES) {
        (void)fclose(f);
        sa_log_error("config", "config file exceeds size cap",
            SA_LOG_KV("path", path),
            SA_LOG_KV_INT("size_bytes", (long long)pos),
            SA_LOG_KV_INT("max_bytes", (long long)SA_CFG_MAX_BYTES),
            SA_LOG_END);
        return SA_ERR_TOOBIG;
    }
    if (fseek(f, 0, SEEK_SET) != 0) {
        sa_err_t e = sa_err_from_errno(errno);
        (void)fclose(f);
        return e;
    }

    size_t len = (size_t)pos;
    char *buf = malloc(len + 1);
    if (!buf) {
        (void)fclose(f);
        return SA_ERR_NOMEM;
    }
    size_t got = fread(buf, 1, len, f);
    (void)fclose(f);
    if (got != len) {
        free(buf);
        return SA_ERR_IO;
    }
    buf[got] = '\0';

    sa_err_t r = sa_config_load_buf(buf, got, out);
    free(buf);
    return r;
}
