"""
Provider Configuration Loader - Load and validate cloud provider configuration.

Loads YAML configuration files with environment variable substitution,
validates required fields, and prepares configuration for the Go adapter.

Supports dynamic provider switching without code changes.
"""

import json
import logging
import os
import re
from typing import Any, Dict, Optional

import yaml

logger = logging.getLogger(__name__)

# Environment variable pattern: ${VAR_NAME} or $VAR_NAME
ENV_VAR_PATTERN = re.compile(r"\$\{([^}]+)\}|\$([A-Z_][A-Z0-9_]*)")

# Required configuration fields per service type
REQUIRED_FIELDS = {
    "storage": ["provider", "bucket_name"],
    "pubsub": ["provider", "topic_name"],
    "database": ["provider", "table_name"],
    "secrets": ["provider"],
}

# Valid providers per service type
VALID_PROVIDERS = {
    "storage": ["aws", "azure", "gcp", "alibaba", "huawei", "local"],
    "pubsub": ["aws", "azure", "gcp", "nats", "rocketmq", "local"],
    "database": ["aws", "azure", "alibaba", "huawei", "local"],
    "secrets": ["aws", "azure", "gcp", "vault", "local"],
}


class ConfigError(Exception):
    """Raised when configuration is invalid or cannot be loaded."""

    pass


def load_provider_config(config_path: str = "config/provider.yaml") -> Dict[str, Any]:
    """Load provider configuration from YAML file.

    Args:
        config_path: Path to the YAML configuration file.

    Returns:
        Dictionary with validated configuration and environment variables substituted.

    Raises:
        ConfigError: If configuration is invalid or file cannot be read.
    """
    logger.info(f"Loading provider configuration from: {config_path}")

    # Read YAML file
    try:
        with open(config_path, "r") as f:
            config = yaml.safe_load(f)
    except FileNotFoundError:
        raise ConfigError(f"Configuration file not found: {config_path}")
    except yaml.YAMLError as e:
        raise ConfigError(f"Failed to parse YAML configuration: {e}")
    except IOError as e:
        raise ConfigError(f"Failed to read configuration file: {e}")

    if not isinstance(config, dict):
        raise ConfigError("Configuration must be a YAML mapping")

    # Substitute environment variables
    config = substitute_env_vars(config)

    # Validate configuration
    validate_config(config)

    logger.info(f"Successfully loaded configuration for providers: {list(config.keys())}")

    return config


def substitute_env_vars(config: Any) -> Any:
    """Recursively substitute environment variables in configuration values.

    Supports ${VAR_NAME} and $VAR_NAME patterns.
    If environment variable is not set, keeps the original pattern.

    Args:
        config: Configuration value (dict, list, str, or other).

    Returns:
        Configuration with environment variables substituted.
    """
    if isinstance(config, dict):
        return {k: substitute_env_vars(v) for k, v in config.items()}
    elif isinstance(config, list):
        return [substitute_env_vars(item) for item in config]
    elif isinstance(config, str):
        return _replace_env_vars(config)
    else:
        return config


def _replace_env_vars(value: str) -> str:
    """Replace environment variable patterns in a string.

    Args:
        value: String potentially containing ${VAR} or $VAR patterns.

    Returns:
        String with environment variables substituted.
    """

    def replacer(match):
        var_name = match.group(1) or match.group(2)
        env_value = os.environ.get(var_name)
        if env_value is not None:
            return env_value
        # Keep original if not found
        return match.group(0)

    return ENV_VAR_PATTERN.sub(replacer, value)


def validate_config(config: Dict[str, Any]) -> None:
    """Validate configuration structure and required fields.

    Args:
        config: Configuration dictionary to validate.

    Raises:
        ConfigError: If validation fails.
    """
    errors = []

    # Check each service type
    for service_type in ["storage", "pubsub", "database", "secrets"]:
        if service_type in config:
            service_config = config[service_type]
            if not isinstance(service_config, dict):
                errors.append(f"'{service_type}' configuration must be a mapping")
                continue

            # Check required fields
            for field in REQUIRED_FIELDS.get(service_type, []):
                if field not in service_config:
                    errors.append(f"Missing required field '{field}' in '{service_type}' config")

            # Validate provider if present
            provider = service_config.get("provider")
            if provider:
                valid_providers = VALID_PROVIDERS.get(service_type, [])
                if provider not in valid_providers:
                    errors.append(
                        f"Invalid provider '{provider}' for '{service_type}'. "
                        f"Valid providers: {valid_providers}"
                    )

    if errors:
        raise ConfigError("Configuration validation failed:\n" + "\n".join(f"- {e}" for e in errors))


def config_to_json(config: Dict[str, Any]) -> str:
    """Convert configuration to JSON string for passing to Go adapter.

    Args:
        config: Configuration dictionary.

    Returns:
        JSON string representation.
    """
    try:
        return json.dumps(config, default=str)
    except (TypeError, ValueError) as e:
        raise ConfigError(f"Failed to serialize configuration to JSON: {e}")


def get_service_config(config: Dict[str, Any], service_type: str) -> Dict[str, Any]:
    """Extract configuration for a specific service type.

    Args:
        config: Full configuration dictionary.
        service_type: Service type to extract ('storage', 'pubsub', 'database', 'secrets').

    Returns:
        Service-specific configuration dictionary.

    Raises:
        ConfigError: If service type not found in configuration.
    """
    if service_type not in config:
        raise ConfigError(f"Service type '{service_type}' not found in configuration")

    return config[service_type]


def create_provider_url(service_type: str, config: Dict[str, Any]) -> str:
    """Create provider URL from configuration for Go CDK.

    Converts configuration into the URL format expected by Go CDK.

    Args:
        service_type: Type of service ('storage', 'pubsub', 'database', 'secrets').
        config: Service-specific configuration dictionary.

    Returns:
        Provider URL string (e.g., 's3://bucket-name', 'nats://localhost:4222').

    Raises:
        ConfigError: If required configuration is missing.
    """
    provider = config.get("provider", "local")

    if service_type == "storage":
        bucket_name = config.get("bucket_name", "")
        if provider == "aws":
            return f"s3://{bucket_name}"
        elif provider == "azure":
            container = config.get("container_name", bucket_name)
            return f"azblob://{container}"
        elif provider == "gcp":
            return f"gs://{bucket_name}"
        elif provider == "alibaba":
            return f"alioss://{bucket_name}"
        elif provider == "huawei":
            return f"huaweios://{bucket_name}"
        else:  # local
            endpoint = config.get("endpoint", "http://localhost:9000")
            return f"s3://{bucket_name}?endpoint={endpoint}&disableSSL=true&s3ForcePathStyle=true"

    elif service_type == "pubsub":
        topic_name = config.get("topic_name", "")
        if provider == "aws":
            return f"awssqs://{topic_name}"
        elif provider == "azure":
            return f"azuresb://{topic_name}"
        elif provider == "gcp":
            return f"gcppubsub://{topic_name}"
        elif provider == "nats":
            server = config.get("server", "localhost:4222")
            return f"nats://{server}/{topic_name}"
        else:  # local
            server = config.get("server", "localhost:4222")
            return f"nats://{server}/{topic_name}"

    elif service_type == "database":
        table_name = config.get("table_name", "")
        if provider == "aws":
            return f"dynamodb://{table_name}"
        elif provider == "azure":
            return f"azurecosmosdb://{table_name}"
        elif provider == "alibaba":
            return f"tablestore://{table_name}"
        elif provider == "huawei":
            return f"gaussdb://{table_name}"
        else:  # local
            endpoint = config.get("endpoint", "http://localhost:8000")
            return f"dynamodb://{table_name}?endpoint={endpoint}&disableSSL=true"

    elif service_type == "secrets":
        if provider == "aws":
            return "awssecretsmanager://"
        elif provider == "azure":
            vault_url = config.get("vault_url", "")
            return f"azurekeyvault://{vault_url}"
        elif provider == "gcp":
            project_id = config.get("project_id", "")
            return f"gcpsecretmanager://{project_id}"
        elif provider == "vault":
            addr = config.get("address", "http://localhost:8200")
            return f"hashivault://{addr}"
        else:  # local
            return "redis://localhost:6379"

    else:
        raise ConfigError(f"Unknown service type: {service_type}")


def load_and_prepare_config(config_path: str = "config/provider.yaml") -> Dict[str, Any]:
    """Load configuration, validate, and prepare for Go adapter.

    Convenience function that combines loading, validation, and URL creation.
    Applies environment variable overrides after loading YAML config.

    Args:
        config_path: Path to YAML configuration file.

    Returns:
        Prepared configuration dictionary with URLs for each service.
    """
    # Load and validate
    config = load_provider_config(config_path)
    
    # Apply environment variable overrides
    config = apply_env_overrides(config)

    # Create provider URLs
    prepared = {}
    for service_type in ["storage", "pubsub", "database", "secrets"]:
        if service_type in config:
            service_config = config[service_type]
            try:
                url = create_provider_url(service_type, service_config)
                prepared[service_type] = {
                    "url": url,
                    "config": service_config,
                }
            except ConfigError as e:
                logger.warning(f"Could not create URL for {service_type}: {e}")
                prepared[service_type] = {"config": service_config}

    return prepared


def apply_env_overrides(config: Dict[str, Any]) -> Dict[str, Any]:
    """Override configuration with environment variables.

    Environment variables take precedence over YAML config values.
    Supported overrides:
    - CLOUD_PROVIDER: Overrides provider for all services
    - STORAGE_ENDPOINT: Overrides storage endpoint
    - STORAGE_BUCKET: Overrides storage bucket name
    - PUBSUB_SERVER: Overrides pub/sub server address
    - DATABASE_ENDPOINT: Overrides database endpoint
    - SECRETS_ADDRESS: Overrides secrets manager address

    Args:
        config: Configuration dictionary loaded from YAML.

    Returns:
        Configuration with environment variable overrides applied.
    """
    import copy
    overridden_config = copy.deepcopy(config)

    # Override cloud provider for all services
    cloud_provider = os.getenv("CLOUD_PROVIDER")
    if cloud_provider:
        logger.info(f"Overriding cloud provider via CLOUD_PROVIDER={cloud_provider}")
        for service_type in ["storage", "pubsub", "database", "secrets"]:
            if service_type in overridden_config:
                overridden_config[service_type]["provider"] = cloud_provider

    # Override storage-specific settings
    storage_endpoint = os.getenv("STORAGE_ENDPOINT")
    if storage_endpoint and "storage" in overridden_config:
        logger.info(f"Overriding storage endpoint via STORAGE_ENDPOINT={storage_endpoint}")
        overridden_config["storage"]["endpoint"] = storage_endpoint

    storage_bucket = os.getenv("STORAGE_BUCKET")
    if storage_bucket and "storage" in overridden_config:
        logger.info(f"Overriding storage bucket via STORAGE_BUCKET={storage_bucket}")
        overridden_config["storage"]["bucket_name"] = storage_bucket

    # Override pub/sub settings
    pubsub_server = os.getenv("PUBSUB_SERVER")
    if pubsub_server and "pubsub" in overridden_config:
        logger.info(f"Overriding pub/sub server via PUBSUB_SERVER={pubsub_server}")
        overridden_config["pubsub"]["server"] = pubsub_server

    # Override database settings
    database_endpoint = os.getenv("DATABASE_ENDPOINT")
    if database_endpoint and "database" in overridden_config:
        logger.info(f"Overriding database endpoint via DATABASE_ENDPOINT={database_endpoint}")
        overridden_config["database"]["endpoint"] = database_endpoint

    # Override secrets settings
    secrets_address = os.getenv("SECRETS_ADDRESS")
    if secrets_address and "secrets" in overridden_config:
        logger.info(f"Overriding secrets address via SECRETS_ADDRESS={secrets_address}")
        overridden_config["secrets"]["address"] = secrets_address

    return overridden_config
