# Match the Liquibase and MySQL JDBC versions used by cd-pipeline.yml.
FROM liquibase/liquibase:4.29.2

# The upstream image does not bundle the MySQL driver.
ADD --checksum=sha256:94e7fa815370cdcefed915db7f53f88445fac110f8c3818392b992ec9ee6d295 --chmod=0644 https://repo.maven.apache.org/maven2/com/mysql/mysql-connector-j/8.3.0/mysql-connector-j-8.3.0.jar /liquibase/lib/mysql-connector-j-8.3.0.jar

# Set working directory and copy migration changelogs and seed data for ACA Job execution
WORKDIR /liquibase/changelog
COPY . /liquibase/changelog/
