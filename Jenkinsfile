@Library(['shared-libs']) _
 
pipeline {

    agent {
        dockerfile {
            label 'docker'
            filename 'Dockerfile'
            args '-v /etc/passwd:/etc/passwd:ro -v /var/run/docker.sock:/var/run/docker.sock:rw'
        }
    }
 
    options {
        ansiColor('xterm')
        timestamps()
        timeout(time: 1, unit: 'HOURS')
        gitLabConnection('GitLab Master')
        buildDiscarder(logRotator(numToKeepStr: '100', artifactNumToKeepStr: '10'))
    }
 
    environment {
        HOME="${WORKSPACE}"
        PYTHONUNBUFFERED=1
    }
 
    parameters {
        string(name: 'REF', defaultValue: '\${gitlabBranch}', description: 'Commit to build')
    }
 
    stages {
        stage('Prep') {
            steps {
                script {
                    updateGitlabCommitStatus(name: 'Jenkins CI', state: 'running')
                }
            }
        }
        stage('Compile') {
            steps {
                echo "building"
                sh "make binary"
            }
        }
	stage('Test') {
            steps {
                echo "Running tests"
		// Tests require supported GPU
                // make test-main
                sh "make check-format"
            }
        }
    }
    post {
        always {
            script{
                String status = (currentBuild.currentResult == "SUCCESS") ? "success" : "failed"
                updateGitlabCommitStatus(name: 'Jenkins CI', state: status)
            }
        }
        cleanup {
            cleanWs()
        }
    }
}
