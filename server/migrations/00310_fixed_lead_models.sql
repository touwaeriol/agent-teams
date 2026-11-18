-- +goose Up
-- The coordinator and thread leads always run the default model (orchestrator.Model: the owner's
-- default for the runtime, then the built-in one); /model, the project's model settings and the
-- role's model only reach members. Clear what the old settings left so the data says the same:
-- the model of coordinator and lead roles (templates and project copies), the project coordinator
-- model (the column stays: the API still returns coordinator_model, always ''), the /model
-- override of coordinators and leads, and the model in their copy of the role (at_employees.spec).
UPDATE at_roles SET model = '' WHERE tier IN ('coordinator', 'lead') AND model <> '';
UPDATE at_projects SET coordinator_model = '' WHERE coordinator_model <> '';
UPDATE at_employees SET model = '' WHERE tier IN ('coordinator', 'lead') AND model <> '';
UPDATE at_employees SET spec = jsonb_set(spec, '{model}', '""')
 WHERE tier IN ('coordinator', 'lead') AND COALESCE(spec->>'model', '') <> '';

-- +goose Down
-- Nothing to undo: the cleared models are not recorded, and '' (follow the defaults) is valid in
-- the old schema too.
SELECT 1;
