--
-- PostgreSQL database dump
--


-- Dumped from database version 17.11
-- Dumped by pg_dump version 17.11

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: at_conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_conversations (
    id text NOT NULL,
    user_id text,
    title text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    history jsonb DEFAULT '{}'::jsonb NOT NULL,
    project_id text,
    plan jsonb DEFAULT '[]'::jsonb NOT NULL
);


--
-- Name: at_git_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_git_connections (
    id text NOT NULL,
    user_id text NOT NULL,
    name text NOT NULL,
    provider text DEFAULT 'github'::text NOT NULL,
    login text DEFAULT ''::text NOT NULL,
    token_secret text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    kind text DEFAULT 'token'::text NOT NULL,
    installation_id bigint
);


--
-- Name: at_mail; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_mail (
    conversation_id text NOT NULL,
    id bigint NOT NULL,
    sender text NOT NULL,
    recipient text NOT NULL,
    body text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    read_by jsonb DEFAULT '[]'::jsonb NOT NULL
);


--
-- Name: at_mail_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.at_mail_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: at_mail_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.at_mail_id_seq OWNED BY public.at_mail.id;


--
-- Name: at_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_messages (
    id bigint NOT NULL,
    conversation_id text NOT NULL,
    role text NOT NULL,
    content text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: at_messages_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.at_messages_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: at_messages_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.at_messages_id_seq OWNED BY public.at_messages.id;


--
-- Name: at_nodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_nodes (
    id text NOT NULL,
    user_id text NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: at_projects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_projects (
    id text NOT NULL,
    user_id text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    context text DEFAULT ''::text NOT NULL,
    repo text,
    branch text DEFAULT ''::text NOT NULL,
    git_connection_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    archived boolean DEFAULT false NOT NULL
);


--
-- Name: at_questions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_questions (
    conversation_id text NOT NULL,
    id text NOT NULL,
    asker text NOT NULL,
    task_id text,
    audience text NOT NULL,
    kind text NOT NULL,
    text text NOT NULL,
    options jsonb DEFAULT '[]'::jsonb NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    answer text,
    status text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    answered_at timestamp with time zone
);


--
-- Name: at_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_sessions (
    token_hash text NOT NULL,
    user_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: at_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_settings (
    key text NOT NULL,
    value jsonb NOT NULL
);


--
-- Name: at_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_tasks (
    conversation_id text NOT NULL,
    id text NOT NULL,
    assignee text NOT NULL,
    instruction text NOT NULL,
    depends_on jsonb NOT NULL,
    status text NOT NULL,
    output text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    parent text DEFAULT ''::text NOT NULL
);


--
-- Name: at_teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_teams (
    user_id text NOT NULL,
    config text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: at_users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.at_users (
    id text NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    role text DEFAULT 'user'::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    max_nodes integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: at_mail id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_mail ALTER COLUMN id SET DEFAULT nextval('public.at_mail_id_seq'::regclass);


--
-- Name: at_messages id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_messages ALTER COLUMN id SET DEFAULT nextval('public.at_messages_id_seq'::regclass);


--
-- Name: at_conversations at_conversations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_conversations
    ADD CONSTRAINT at_conversations_pkey PRIMARY KEY (id);


--
-- Name: at_git_connections at_git_connections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_git_connections
    ADD CONSTRAINT at_git_connections_pkey PRIMARY KEY (id);


--
-- Name: at_git_connections at_git_connections_user_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_git_connections
    ADD CONSTRAINT at_git_connections_user_id_name_key UNIQUE (user_id, name);


--
-- Name: at_mail at_mail_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_mail
    ADD CONSTRAINT at_mail_pkey PRIMARY KEY (conversation_id, id);


--
-- Name: at_messages at_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_messages
    ADD CONSTRAINT at_messages_pkey PRIMARY KEY (id);


--
-- Name: at_nodes at_nodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_nodes
    ADD CONSTRAINT at_nodes_pkey PRIMARY KEY (id);


--
-- Name: at_nodes at_nodes_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_nodes
    ADD CONSTRAINT at_nodes_token_hash_key UNIQUE (token_hash);


--
-- Name: at_nodes at_nodes_user_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_nodes
    ADD CONSTRAINT at_nodes_user_id_name_key UNIQUE (user_id, name);


--
-- Name: at_projects at_projects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_projects
    ADD CONSTRAINT at_projects_pkey PRIMARY KEY (id);


--
-- Name: at_projects at_projects_user_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_projects
    ADD CONSTRAINT at_projects_user_id_name_key UNIQUE (user_id, name);


--
-- Name: at_questions at_questions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_questions
    ADD CONSTRAINT at_questions_pkey PRIMARY KEY (conversation_id, id);


--
-- Name: at_sessions at_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_sessions
    ADD CONSTRAINT at_sessions_pkey PRIMARY KEY (token_hash);


--
-- Name: at_settings at_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_settings
    ADD CONSTRAINT at_settings_pkey PRIMARY KEY (key);


--
-- Name: at_tasks at_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_tasks
    ADD CONSTRAINT at_tasks_pkey PRIMARY KEY (conversation_id, id);


--
-- Name: at_teams at_teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_teams
    ADD CONSTRAINT at_teams_pkey PRIMARY KEY (user_id);


--
-- Name: at_users at_users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_users
    ADD CONSTRAINT at_users_email_key UNIQUE (email);


--
-- Name: at_users at_users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_users
    ADD CONSTRAINT at_users_pkey PRIMARY KEY (id);


--
-- Name: at_conversations_project; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX at_conversations_project ON public.at_conversations USING btree (project_id, created_at);


--
-- Name: at_conversations_user; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX at_conversations_user ON public.at_conversations USING btree (user_id, created_at);


--
-- Name: at_messages_conversation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX at_messages_conversation ON public.at_messages USING btree (conversation_id, id);


--
-- Name: at_conversations at_conversations_project_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_conversations
    ADD CONSTRAINT at_conversations_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.at_projects(id) ON DELETE CASCADE;


--
-- Name: at_conversations at_conversations_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_conversations
    ADD CONSTRAINT at_conversations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- Name: at_git_connections at_git_connections_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_git_connections
    ADD CONSTRAINT at_git_connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- Name: at_mail at_mail_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_mail
    ADD CONSTRAINT at_mail_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.at_conversations(id) ON DELETE CASCADE;


--
-- Name: at_messages at_messages_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_messages
    ADD CONSTRAINT at_messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.at_conversations(id) ON DELETE CASCADE;


--
-- Name: at_nodes at_nodes_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_nodes
    ADD CONSTRAINT at_nodes_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- Name: at_projects at_projects_git_connection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_projects
    ADD CONSTRAINT at_projects_git_connection_id_fkey FOREIGN KEY (git_connection_id) REFERENCES public.at_git_connections(id) ON DELETE SET NULL;


--
-- Name: at_projects at_projects_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_projects
    ADD CONSTRAINT at_projects_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- Name: at_questions at_questions_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_questions
    ADD CONSTRAINT at_questions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.at_conversations(id) ON DELETE CASCADE;


--
-- Name: at_sessions at_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_sessions
    ADD CONSTRAINT at_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- Name: at_tasks at_tasks_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_tasks
    ADD CONSTRAINT at_tasks_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.at_conversations(id) ON DELETE CASCADE;


--
-- Name: at_teams at_teams_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.at_teams
    ADD CONSTRAINT at_teams_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.at_users(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--


